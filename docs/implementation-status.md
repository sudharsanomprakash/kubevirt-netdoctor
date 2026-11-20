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

## Kernel observability (branch `feature/kernel-observability`, draft PR #9)

| Feature | Status | Evidence |
| --- | --- | --- |
| eBPF tracer: drops with kernel drop reason and location | Done | `internal/ebpf` real-kernel test `TestTraceUDPClosedPortDrop` |
| eBPF tracer: TCP retransmits, resets sent/received, state changes | Done | `TestTraceTCPResetToClosedPort`, `TestTraceTCPRetransmitWhenPeerLinkDown` |
| Per-interface rx/tx packet counts | Done | asserted in `TestTraceUDPClosedPortDrop` |
| In-kernel filter (netns+ifindex, IPv4/IPv6 address) | Done | `TestTraceFilterExcludesOtherTraffic` |
| Resource controls: duration cap, max events, bounded ring buffer, lost-event accounting | Done | `TestTraceLostEventsAreCounted` (recorded+suppressed+lost == kernel counter) |
| Overhead measurement (BPF run-time stats) | Done | reported per trace; ~0.008% of one core in the e2e test |
| Graceful degradation: missing tracepoints skipped, unprivileged refusal | Done | `host capabilities`; manual run as uid 65534 without capabilities |
| Trace plan (`trace vm/x`), node `host trace --plan`, attribution levels | Done | `TestTraceEndToEnd` (simulated launcher netns + pod cgroup), `TestHostTraceWithPlan` |
| `diagnose --trace` findings (TRACE_*) | Done | golden `diagnose-trace.txt`, `TestDiagnoseTraceMismatch` |
| `capabilities` (cluster) and `host capabilities` (node, verifier load) | Done | golden `capabilities.txt`, `TestCapabilitiesDegraded`, real verifier load on 6.18 |
| `topology -o dot|mermaid` | Done | goldens validated with Graphviz and the Mermaid parser |
| CI: BPF -Werror compile, bindings drift, real-kernel tests as root | Done (local) / pending first CI run | PR #9 |
| Live validation against a KubeVirt virt-launcher pod | Blocked | No KubeVirt cluster here; needs the owner's cluster |

## Not started

| Feature | Notes |
| --- | --- |
| UDP/DNS, conntrack/routing, QEMU/vhost, OVS datapath hooks | Next eBPF hook groups |
| `report` bundle and `--fail-on` exit codes | |
| Opt-in connectivity `probe` | Must be explicit and labeled with its origin |
| OVN-Kubernetes / Kube-OVN adapters | Design in PR #7 |
| Docs: architecture, eBPF internals, CLI reference, security model | |

## Resuming

`git checkout feature/kernel-observability && PATH=$HOME/go/bin:$PATH make verify && make bpf-test` (root).
