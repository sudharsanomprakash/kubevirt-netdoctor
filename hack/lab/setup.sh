#!/usr/bin/env bash
# Builds a single-node KubeVirt lab with VLAN networking on an Ubuntu 24.04
# VM that has nested virtualization (/dev/kvm). Run as root on the lab VM:
#
#   sudo hack/lab/setup.sh            # everything (idempotent where possible)
#   sudo hack/lab/setup.sh network    # only the simulated switch/uplinks
#
# Topology (all on one host; "upstream" is a network namespace standing in
# for the physical switch and router):
#
#   VM (bridge binding) -- br1 (Linux bridge, vlan_filtering) -- bond0 --+-- veth-a1 ==\
#                                                                         \-- veth-a2 ==+= upstream "sw" bridge
#   VM (OVS CNI)        -- br-ovs (OVS) ----------------------- veth-o ============/      sw.100 192.168.100.1/24
#                                                                                         sw.200 192.168.200.1/24
#
# Nothing here is suitable for production; it is a disposable test lab.
set -euo pipefail

K8S_MINOR="${K8S_MINOR:-v1.33}"
GO_VERSION="${GO_VERSION:-1.26.10}"
LOG() { printf '\n=== %s\n' "$*"; }

need_root() { [ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }; }

packages() {
	LOG "packages"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -q
	apt-get install -y -q containerd openvswitch-switch jq curl ca-certificates gpg \
		iproute2 bridge-utils conntrack ethtool tcpdump make git
	[ -e /dev/kvm ] || { echo "/dev/kvm missing: the VM needs nested virtualization" >&2; exit 1; }
}

kernel() {
	LOG "kernel modules and sysctls"
	for m in 8021q bonding openvswitch br_netfilter overlay; do modprobe "$m"; done
	printf '%s\n' 8021q bonding openvswitch br_netfilter overlay >/etc/modules-load.d/netdoctor-lab.conf
	cat >/etc/sysctl.d/90-netdoctor-lab.conf <<-EOF
		net.ipv4.ip_forward = 1
		net.bridge.bridge-nf-call-iptables = 1
		net.bridge.bridge-nf-call-ip6tables = 1
	EOF
	sysctl -q --system
	swapoff -a
}

network() {
	LOG "simulated switch, bond, Linux bridge and OVS bridge"
	ip netns add upstream 2>/dev/null || true
	U="ip -n upstream"

	# Upstream switch: a VLAN-aware bridge with router interfaces per VLAN.
	$U link add sw type bridge vlan_filtering 1 2>/dev/null || true
	$U link set sw up
	for v in 100 200; do
		$U link add link sw name sw.$v type vlan id $v 2>/dev/null || true
		ip netns exec upstream bridge vlan add dev sw vid $v self
		$U addr replace 192.168.$v.1/24 dev sw.$v
		$U link set sw.$v up
	done

	# Bond of two members, trunked to the switch (VLAN 100 and 200 tagged).
	ip link add bond0 type bond mode active-backup miimon 100 2>/dev/null || true
	for n in 1 2; do
		if ! ip link show veth-a$n >/dev/null 2>&1; then
			ip link add veth-a$n type veth peer name sw-a$n netns upstream
		fi
		ip link set veth-a$n down
		ip link set veth-a$n master bond0 2>/dev/null || true
		ip link set veth-a$n up
		$U link set sw-a$n master sw
		$U link set sw-a$n up
		for v in 100 200; do ip netns exec upstream bridge vlan add dev sw-a$n vid $v; done
	done
	ip link set bond0 up

	# br1: VLAN-filtering Linux bridge for the bridge CNI, uplink bond0.
	ip link add br1 type bridge vlan_filtering 1 2>/dev/null || true
	ip link set bond0 master br1
	bridge vlan add dev bond0 vid 100
	ip link set br1 up

	# br-ovs: OVS bridge for OVS CNI, uplink veth-o as a trunk.
	systemctl enable --now openvswitch-switch >/dev/null
	ovs-vsctl --may-exist add-br br-ovs
	if ! ip link show veth-o >/dev/null 2>&1; then
		ip link add veth-o type veth peer name sw-o netns upstream
	fi
	ovs-vsctl --may-exist add-port br-ovs veth-o -- set port veth-o trunks=200
	ip link set veth-o up
	ip link set br-ovs up
	$U link set sw-o master sw
	$U link set sw-o up
	ip netns exec upstream bridge vlan add dev sw-o vid 200
}

kubernetes() {
	LOG "containerd + kubeadm $K8S_MINOR"
	mkdir -p /etc/containerd
	containerd config default | sed 's/SystemdCgroup = false/SystemdCgroup = true/' >/etc/containerd/config.toml
	systemctl restart containerd
	install -d -m 0755 /etc/apt/keyrings
	curl -fsSL "https://pkgs.k8s.io/core:/stable:/$K8S_MINOR/deb/Release.key" | gpg --batch --yes --dearmor -o /etc/apt/keyrings/kubernetes.gpg
	echo "deb [signed-by=/etc/apt/keyrings/kubernetes.gpg] https://pkgs.k8s.io/core:/stable:/$K8S_MINOR/deb/ /" >/etc/apt/sources.list.d/kubernetes.list
	apt-get update -q
	apt-get install -y -q kubelet kubeadm kubectl
	if [ ! -f /etc/kubernetes/admin.conf ]; then
		kubeadm init --pod-network-cidr=10.244.0.0/16 --node-name "$(hostname -s)"
	fi
	export KUBECONFIG=/etc/kubernetes/admin.conf
	for u in /root "/home/${SUDO_USER:-ubuntu}"; do
		[ -d "$u" ] || continue
		mkdir -p "$u/.kube" && cp /etc/kubernetes/admin.conf "$u/.kube/config"
		chown -R "$(stat -c %U:%G "$u")" "$u/.kube"
	done
	kubectl taint nodes --all node-role.kubernetes.io/control-plane- 2>/dev/null || true
	kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml
	kubectl wait --for=condition=Ready node --all --timeout=300s
}

latest() { curl -fsSL "https://api.github.com/repos/$1/releases/latest" | jq -r .tag_name; }

addons() {
	export KUBECONFIG=/etc/kubernetes/admin.conf
	CNAO="${CNAO_VERSION:-$(latest kubevirt/cluster-network-addons-operator)}"
	LOG "cluster-network-addons-operator $CNAO (Multus, Linux bridge CNI, OVS CNI)"
	base="https://github.com/kubevirt/cluster-network-addons-operator/releases/download/$CNAO"
	for f in namespace.yaml network-addons-config.crd.yaml operator.yaml; do kubectl apply -f "$base/$f"; done
	kubectl -n cluster-network-addons wait --for=condition=Available deploy --all --timeout=300s
	kubectl apply -f - <<-EOF
		apiVersion: networkaddonsoperator.network.kubevirt.io/v1
		kind: NetworkAddonsConfig
		metadata:
		  name: cluster
		spec:
		  multus: {}
		  linuxBridge: {}
		  ovs: {}
		  imagePullPolicy: IfNotPresent
	EOF
	kubectl wait networkaddonsconfig cluster --for=condition=Available --timeout=600s

	KV="${KUBEVIRT_VERSION:-$(curl -fsSL https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)}"
	LOG "KubeVirt $KV"
	kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/$KV/kubevirt-operator.yaml"
	kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/$KV/kubevirt-cr.yaml"
	kubectl -n kubevirt wait kv kubevirt --for=condition=Available --timeout=900s
	echo "$KV" >/var/lib/netdoctor-lab-kubevirt-version
	{
		echo "kubernetes: $(kubectl version -o json | jq -r .serverVersion.gitVersion)"
		echo "cnao: $CNAO"
		echo "kubevirt: $KV"
		echo "kernel: $(uname -r)"
	} | tee /var/lib/netdoctor-lab-versions
}

netdoctor() {
	LOG "build netdoctor (Go $GO_VERSION)"
	if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go$GO_VERSION"; then
		curl -fsSL "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" -o /tmp/go.tgz
		rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
	fi
	src="$(cd "$(dirname "$0")/../.." && pwd)"
	(cd "$src" && PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local make build)
	install -m 0755 "$src/bin/kubectl-netdoctor" /usr/local/bin/kubectl-netdoctor
	kubectl-netdoctor version
}

need_root
case "${1:-all}" in
network) kernel; network ;;
netdoctor) netdoctor ;;
all) packages; kernel; network; kubernetes; addons; netdoctor
	LOG "done: run 'sudo hack/lab/workloads.sh' next" ;;
*) echo "usage: $0 [all|network|netdoctor]" >&2; exit 2 ;;
esac
