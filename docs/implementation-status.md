# Implementation status

This file is the resumable record of what is implemented, how it is validated and what
is blocked. A feature is **Done** only when code and tests exist. Proposals and
interfaces do not count.

Status values: **Done**, **In progress**, **Not started**, **Blocked** (with the external
reason).

## Environment used for validation

| Item | Value |
| --- | --- |
| Build/test host | Linux 6.18 (Firecracker microVM), root, all capabilities |
| Kernel features present | BPF syscall + JIT, BTF (`/sys/kernel/btf/vmlinux`), tracepoints `skb:kfree_skb`, `tcp:*`, `sock:inet_sock_set_state`, `net:*`, veth, Linux bridge, TUN, netfilter/conntrack |
| Kernel features absent | 802.1Q VLAN devices, bonding, Open vSwitch kernel module, bridge VLAN filtering |
| Kubernetes / KubeVirt | **None available**: KubeVirt end-to-end scenarios cannot be executed here |
| Toolchain | Go 1.26.10, clang 18, libbpf 1.3 headers |

## Phase 1: audit and stabilize

| Feature | Status | Evidence |
| --- | --- | --- |
| Baseline tests on `main` (unit, golden, fake-cluster, race) | Done | `go test -race ./...` passes on `0355482` |
| PR #8 (k8s.io 0.37.1 → 0.37.2) | Validated, awaiting owner merge | Builds; full race test suite passes locally; CI green |
| OVS false-positive regression (older OVS CNI without `contPodUid`) | Done (synthetic) / Blocked (live) | Scenario tests `older OVS CNI without pod UIDs: *`; live re-run needs the owner's cluster |
| v0.1.0 release | Blocked | Requires owner approval to tag and publish |

## Later phases

Tracked below as work lands. See the sections that follow for each engine.
