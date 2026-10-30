#!/usr/bin/env bash
# Runs the host collector integration test against real iproute2 output in a
# disposable network namespace. Requires root (CAP_NET_ADMIN) and iproute2.
# Everything it creates lives in the namespaces below and is removed on exit.
set -euo pipefail

NS=netdoctor-it
PEER=netdoctor-it-peer
cleanup() { ip netns del "$NS" 2>/dev/null || true; ip netns del "$PEER" 2>/dev/null || true; }
trap cleanup EXIT
cleanup

ip netns add "$NS"
ip netns add "$PEER"
ip -n "$NS" link set lo up
# Some kernels (for example in sandboxes) lack bridge VLAN filtering; the
# VLAN assertions are then skipped rather than faked.
VLAN=1
if ! ip -n "$NS" link add br1 type bridge vlan_filtering 1 2>/dev/null; then
  echo "bridge VLAN filtering is not supported by this kernel; VLAN checks will be skipped" >&2
  VLAN=0
  ip -n "$NS" link add br1 type bridge
fi
ip -n "$NS" link set br1 mtu 1400
ip -n "$NS" link add vethhost type veth peer name vethpod
ip -n "$NS" link set vethpod netns "$PEER"
ip -n "$NS" link set vethhost master br1
if [ "$VLAN" = 1 ]; then
  bridge -n "$NS" vlan add dev vethhost vid 100 pvid untagged
  bridge -n "$NS" vlan add dev vethhost vid 200-210
fi
ip -n "$NS" link set br1 up
ip -n "$NS" link set vethhost up
ip -n "$NS" addr add 192.0.2.10/24 dev br1
ip -n "$NS" route add default via 192.0.2.1 dev br1
ip -n "$NS" route add blackhole 198.51.100.0/24

cd "$(dirname "$0")/.."
ip netns exec "$NS" env NETDOCTOR_INTEGRATION=1 NETDOCTOR_INTEGRATION_VLAN="$VLAN" go test -count=1 -run TestIntegrationCollect -v ./internal/host/
