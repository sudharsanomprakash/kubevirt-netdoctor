#!/usr/bin/env bash
# Runs netdoctor acceptance scenarios against the lab built by setup.sh and
# workloads.sh. Each fault is injected on the lab host only, checked, and
# reverted; the baseline is re-checked after every revert.
#
#   sudo hack/lab/run-tests.sh            # all scenarios
#   sudo hack/lab/run-tests.sh trace      # one scenario
#
# Results (JSON reports, snapshots, traces, a summary and a redacted bundle)
# are written to ./lab-results/<timestamp>/. Exit status is non-zero if any
# assertion failed. Lab only: never run this against a real cluster.
set -uo pipefail
export KUBECONFIG="${KUBECONFIG:-/etc/kubernetes/admin.conf}"
ND=kubectl-netdoctor
NS=lab
OUT="${OUT:-$PWD/lab-results/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$OUT"
SUMMARY="$OUT/summary.txt"
PASS=0 FAIL=0

log() { printf '%s\n' "$*" | tee -a "$SUMMARY"; }
pass() { PASS=$((PASS + 1)); log "  PASS  $*"; }
fail() { FAIL=$((FAIL + 1)); log "  FAIL  $*"; }

snapshot() { $ND host collect -f "$OUT/$1.snapshot.json" >/dev/null 2>>"$OUT/stderr.log"; echo "$OUT/$1.snapshot.json"; }

# diag NAME VM [extra args...] -> writes $OUT/NAME.json
diag() {
	local name="$1" vm="$2"; shift 2
	$ND diagnose "vm/$vm" -n "$NS" -o json "$@" >"$OUT/$name.json" 2>>"$OUT/stderr.log"
	local rc=$?
	if [ "$rc" -eq 1 ]; then fail "$name: diagnose could not run (see stderr.log)"; return 1; fi
	return 0
}
ids() { jq -r '.findings[].id' "$OUT/$1.json" | sort -u | tr '\n' ' '; }
has() { jq -e --arg id "$2" '.findings | any(.id == $id)' "$OUT/$1.json" >/dev/null; }
errors() { jq '[.findings[] | select(.severity == "Error")] | length' "$OUT/$1.json"; }

expect() { # expect NAME ID [ID...]: at least one of the IDs is present
	local name="$1"; shift
	for id in "$@"; do
		if has "$name" "$id"; then pass "$name: $id reported"; return 0; fi
	done
	fail "$name: expected one of [$*], got [$(ids "$name")]"
}

baseline() { # baseline LABEL: no Error findings on any lab VM
	local label="$1" snap vm
	snap="$(snapshot "$label")"
	for vm in vm-masq vm-bridge vm-ovs; do
		diag "$label-$vm" "$vm" --host-snapshot "$snap" || continue
		if [ "$(errors "$label-$vm")" -eq 0 ]; then
			pass "$label: $vm has no Error findings (findings: $(ids "$label-$vm"))"
		else
			fail "$label: $vm has Error findings: $(jq -c '[.findings[] | select(.severity=="Error") | {id,summary}]' "$OUT/$label-$vm.json")"
		fi
	done
}

ovs_port_of() { # OVS port of a VM's VLAN 200 attachment
	local uid
	uid="$(kubectl -n "$NS" get pod -l "vm.kubevirt.io/name=$1" -o jsonpath='{.items[0].metadata.uid}')"
	ovs-vsctl --no-heading --format=csv --columns=name find interface "external_ids:contPodUid=$uid" 2>/dev/null | head -1 | tr -d '"'
}

scenario_baseline() {
	log "A. healthy baseline"
	baseline baseline
	$ND capabilities -n "$NS" -o json >"$OUT/capabilities.json" && pass "capabilities ran" || fail "capabilities failed"
	$ND host capabilities -o json >"$OUT/host-capabilities.json" 2>>"$OUT/stderr.log"
	if [ "$(jq -r .tracing "$OUT/host-capabilities.json")" = Available ]; then pass "host capabilities: tracing Available"; else fail "host capabilities: $(jq -c . "$OUT/host-capabilities.json")"; fi
	for f in dot mermaid; do
		$ND topology vm/vm-ovs -n "$NS" -o "$f" --host-snapshot "$OUT/baseline.snapshot.json" >"$OUT/topology-vm-ovs.$f" && pass "topology -o $f" || fail "topology -o $f"
	done
}

scenario_nad_missing() {
	log "B. VM referencing a missing NAD"
	kubectl -n "$NS" apply -f - >/dev/null <<-EOF
		apiVersion: kubevirt.io/v1
		kind: VirtualMachine
		metadata: {name: vm-nonad, namespace: $NS}
		spec:
		  runStrategy: Always
		  template:
		    spec:
		      domain:
		        devices:
		          interfaces: [{name: default, masquerade: {}}, {name: ghost, bridge: {}}]
		        resources: {requests: {memory: 128Mi}}
		      networks: [{name: default, pod: {}}, {name: ghost, multus: {networkName: does-not-exist}}]
	EOF
	sleep 20
	diag nad-missing vm-nonad && expect nad-missing NAD_NOT_FOUND
	kubectl -n "$NS" delete vm vm-nonad --wait=false >/dev/null
}

scenario_uplink_vlan() {
	log "C. bridge uplink no longer carries VLAN 100"
	bridge vlan del dev bond0 vid 100
	diag uplink-vlan vm-bridge --host-snapshot "$(snapshot uplink-vlan)" && expect uplink-vlan VLAN_MISMATCH
	bridge vlan add dev bond0 vid 100
}

scenario_vlan_filtering_off() {
	log "D. VLAN filtering disabled on br1"
	ip link set br1 type bridge vlan_filtering 0
	diag vlan-filtering vm-bridge --host-snapshot "$(snapshot vlan-filtering)" && expect vlan-filtering VLAN_NOT_ENFORCED
	ip link set br1 type bridge vlan_filtering 1
	bridge vlan add dev bond0 vid 100
}

scenario_ovs_tag() {
	log "E. OVS port of vm-ovs re-tagged to VLAN 999"
	local port
	port="$(ovs_port_of vm-ovs)"
	if [ -z "$port" ]; then fail "ovs-tag: could not find the OVS port of vm-ovs"; return; fi
	ovs-vsctl set port "$port" tag=999
	diag ovs-tag vm-ovs --host-snapshot "$(snapshot ovs-tag)" && expect ovs-tag VLAN_MISMATCH
	ovs-vsctl set port "$port" tag=200
}

scenario_bridge_down() {
	log "F. host bridge br1 down"
	ip link set br1 down
	diag bridge-down vm-bridge --host-snapshot "$(snapshot bridge-down)" && expect bridge-down HOST_INTERFACE_DOWN
	ip link set br1 up
}

scenario_bond_member() {
	log "G. bond member veth-a2 detached"
	ip link set veth-a2 nomaster
	diag bond-member vm-bridge --host-snapshot "$(snapshot bond-member)" && expect bond-member BOND_MEMBER_DETACHED
	ip link set veth-a2 down; ip link set veth-a2 master bond0; ip link set veth-a2 up
}

scenario_mtu() {
	log "H. MTU expectation (9000) not met by the host path"
	printf 'networks:\n  vlan100:\n    mtu: 9000\n' >"$OUT/expect-mtu.yaml"
	diag mtu vm-bridge --host-snapshot "$(snapshot mtu)" --expectations "$OUT/expect-mtu.yaml" && expect mtu MTU_MISMATCH
}

scenario_trace() {
	log "I. kernel trace of a live virt-launcher pod"
	local plan="$OUT/trace-plan.json" trace="$OUT/trace.json"
	if ! $ND trace vm/vm-bridge -n "$NS" --duration 20s -f "$plan" >"$OUT/trace-plan.txt"; then fail "trace plan"; return; fi
	pass "trace plan created for pod $(jq -r .launcherPod "$plan")"
	# Traffic during the trace: ping the VM, UDP and TCP to closed guest ports,
	# and traffic to an address that only exists while VLAN 100 is broken.
	(
		sleep 4
		kubectl -n "$NS" exec app-vlan100 -- ping -c 10 -i 0.5 192.168.100.10 >/dev/null 2>&1
		kubectl -n "$NS" exec app-vlan100 -- sh -c 'for i in 1 2 3 4 5; do echo x | nc -u -w1 192.168.100.10 9999; done' >/dev/null 2>&1
		kubectl -n "$NS" exec app-vlan100 -- sh -c 'for i in 1 2 3; do nc -z -w1 192.168.100.10 7; done' >/dev/null 2>&1
	) &
	local gen=$!
	if ! $ND host trace --plan "$plan" -f "$trace" 2>"$OUT/trace.stderr"; then
		fail "host trace failed: $(tail -3 "$OUT/trace.stderr")"; wait $gen; return
	fi
	wait $gen
	cat "$OUT/trace.stderr" >>"$SUMMARY"
	local netns ifaces
	netns="$(jq -r .launcherNetns "$trace")"
	ifaces="$(jq '[.interfaces[] | select(.packets > 0)] | length' "$trace")"
	[ "$netns" != 0 ] && [ "$netns" != null ] && pass "launcher pod network namespace found ($netns)" || fail "launcher netns not found"
	[ "$ifaces" -gt 0 ] && pass "packet counts on $ifaces launcher interface/direction(s): $(jq -c '[.interfaces[] | {name,direction,packets}]' "$trace")" \
		|| fail "no packets counted on launcher interfaces"
	[ "$(jq '[.programs[] | select(.attached)] | length' "$trace")" -ge 1 ] && pass "programs attached: $(jq -r '[.programs[] | select(.attached) | .hook] | join(",")' "$trace")" || fail "no program attached"
	log "  info  events by attribution: $(jq -c '[.events[].attribution] | group_by(.) | map({(.[0]): length}) | add' "$trace")"
	log "  info  events by type: $(jq -c '[.events[].type] | group_by(.) | map({(.[0]): length}) | add' "$trace")"
	log "  info  overhead: $(jq -c .overhead "$trace")"
	diag trace-diag vm-bridge --trace "$trace"
	if jq -e '.trace.used' "$OUT/trace-diag.json" >/dev/null; then pass "diagnose --trace used the trace (findings: $(ids trace-diag))"; else fail "diagnose --trace did not use the trace: $(ids trace-diag)"; fi

	log "J. trace with a host-path fault (VLAN 100 removed from the uplink)"
	bridge vlan del dev bond0 vid 100
	$ND trace vm/vm-bridge -n "$NS" --duration 12s -f "$plan" >/dev/null
	( sleep 4; kubectl -n "$NS" exec app-vlan100 -- ping -c 6 -i 0.5 192.168.100.1 >/dev/null 2>&1 ) &
	gen=$!
	$ND host trace --plan "$plan" -f "$OUT/trace-fault.json" 2>>"$OUT/trace.stderr" || fail "host trace (fault) failed"
	wait $gen
	bridge vlan add dev bond0 vid 100
	log "  info  fault trace drops: $(jq -c '[.events[] | select(.type=="drop") | {reason: .dropReason, iface: .interface, attribution, dst}] | unique' "$OUT/trace-fault.json" 2>/dev/null)"
}

scenario_report() {
	log "K. redacted report bundle"
	if $ND report vm/vm-ovs -n "$NS" --host-snapshot "$OUT/baseline.snapshot.json" --bundle "$OUT/report-vm-ovs.tar.gz" --redact >/dev/null 2>>"$OUT/stderr.log"; then
		pass "report bundle written"
		if tar -xzOf "$OUT/report-vm-ovs.tar.gz" | grep -q '192\.168\.200\.10'; then fail "bundle still contains a VM address"; else pass "bundle is redacted"; fi
	else
		fail "report failed"
	fi
}

run() {
	case "$1" in
	baseline) scenario_baseline ;;
	nad) scenario_nad_missing ;;
	uplink) scenario_uplink_vlan; baseline after-uplink ;;
	filtering) scenario_vlan_filtering_off; baseline after-filtering ;;
	ovs) scenario_ovs_tag; baseline after-ovs ;;
	bridge) scenario_bridge_down; baseline after-bridge ;;
	bond) scenario_bond_member; baseline after-bond ;;
	mtu) scenario_mtu ;;
	trace) scenario_trace; baseline after-trace ;;
	report) scenario_report ;;
	*) echo "unknown scenario $1" >&2; exit 2 ;;
	esac
}

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
{
	echo "netdoctor lab run $(date -u +%FT%TZ)"
	$ND version 2>/dev/null | head -1
	cat /var/lib/netdoctor-lab-versions 2>/dev/null
} >"$SUMMARY"
if [ $# -gt 0 ]; then
	for s in "$@"; do run "$s"; done
else
	for s in baseline nad uplink filtering ovs bridge bond mtu trace report; do run "$s"; done
fi
log ""
log "RESULT: $PASS passed, $FAIL failed. Results in $OUT"
[ "$FAIL" -eq 0 ]
