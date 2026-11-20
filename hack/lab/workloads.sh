#!/usr/bin/env bash
# Deploys the lab VMs and application pods and checks that the lab network
# itself works (so later test failures are netdoctor's, not the lab's).
# Run on the lab VM after setup.sh:  sudo hack/lab/workloads.sh
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-/etc/kubernetes/admin.conf}"
dir="$(cd "$(dirname "$0")" && pwd)"
KV="$(cat /var/lib/netdoctor-lab-kubevirt-version)"

kubectl apply -f "$dir/manifests/10-networks.yaml"
sed "s/@KV@/$KV/g" "$dir/manifests/20-vms.yaml.in" | kubectl apply -f -
kubectl apply -f "$dir/manifests/30-apps.yaml"

echo "waiting for VMs and pods..."
for vm in vm-masq vm-bridge vm-ovs; do
	kubectl -n lab wait vmi "$vm" --for=condition=Ready --timeout=600s
done
kubectl -n lab wait pod app-vlan100 app-vlan200 --for=condition=Ready --timeout=300s
sleep 30 # let CirrOS finish its user-data script

check() {
	local desc="$1"; shift
	if "$@" >/dev/null 2>&1; then echo "  ok    $desc"; else echo "  FAIL  $desc"; return 1; fi
}
echo "lab network checks:"
fail=0
check "app-vlan100 -> VLAN 100 gateway" kubectl -n lab exec app-vlan100 -- ping -c2 -W2 192.168.100.1 || fail=1
check "app-vlan100 -> vm-bridge (VLAN 100)" kubectl -n lab exec app-vlan100 -- ping -c2 -W2 192.168.100.10 || fail=1
check "app-vlan200 -> VLAN 200 gateway" kubectl -n lab exec app-vlan200 -- ping -c2 -W2 192.168.200.1 || fail=1
check "app-vlan200 -> vm-ovs (VLAN 200)" kubectl -n lab exec app-vlan200 -- ping -c2 -W2 192.168.200.10 || fail=1
check "VLAN isolation: app-vlan100 cannot reach 192.168.200.1" sh -c '! kubectl -n lab exec app-vlan100 -- ping -c1 -W2 192.168.200.1' || fail=1
kubectl -n lab get vmi -o wide
if [ "$fail" -ne 0 ]; then
	echo "The lab network is not working as designed; fix it before running tests." >&2
	exit 1
fi
echo "lab ready: run 'sudo hack/lab/run-tests.sh' next"
