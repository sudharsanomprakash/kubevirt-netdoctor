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

## Coverage of the master directive (audit 2026-11-21)

This table is an honest map of what is implemented and validated. **The full production
release (directive section 22) is release-blocked.** v0.1.0 is the Phase 1 milestone: an
early pre-release of what is validated today.

| Directive area | Status | Notes |
| --- | --- | --- |
| Agent extensibility requirement | Done (design only, as required) | `docs/architecture-agents.md`; `internal/archtest` enforces a read-only engine; no remediation code |
| 4.1 Kubernetes discovery | Mostly done | VM/VMI, launcher, Multus, NAD, migration state, capabilities. **Missing:** NetworkPolicy discovery |
| 4.2 Host discovery | Partial | Links, bridges, VLANs, bonds, routes (all tables), OVS. **Missing:** policy rules (`ip rule`), neighbor tables, interface counters, sysctls, driver/offload/queue info |
| 4.3 / 5.1–5.2 Kernel: drops, TCP | Done (validated live) | kfree_skb with reason and location; retransmit, resets, state; interface counts. **Missing:** RTT/congestion beyond srtt, TCP timeouts, fragmentation |
| 5.3 UDP/DNS | Not started | |
| 5.4 Routing/conntrack | Not started | |
| 5.5 Softirq/scheduling | Not started | |
| 5.6 QEMU/virtio/vhost | Not started | Launcher netns and tap/k6t interface counts only |
| 5.7 OVS runtime/datapath | Partial | Config and flow summary. **Missing:** datapath stats, upcalls, drops |
| 5.8 eBPF runtime management | Done | Feature detection, CO-RE, bounded buffers, lost-event accounting, overhead, clean detach. Rate limiting is buffer-bound only |
| 6 VM attribution | Done for netns/ifindex/address | Four levels; pod-UID mismatch detection. **Not tested:** migration, ifindex reuse |
| 7 Path reconstruction | Partial | Topology graph with provenance. **Missing:** forwarding evidence per hop |
| 8 CNI adapters | Partial | OVS and Linux bridge validated live; macvlan/ipvlan/SR-IOV/host-device partial; OVN-K and Kube-OVN designed only (proposal 0002); Calico/Cilium/Flannel route view only |
| 10 Active probes (`probe`) | Not started | |
| 11 Correlation engine | Partial | Rules with evidence, categories and attribution. **Missing:** a unified evidence-record schema and timelines |
| 12 CLI | Mostly done | inspect, topology, diagnose, trace, report, capabilities, version, `--fail-on`. **Missing:** `probe` |
| 13 Host collector | Partial | Snapshot, trace, capabilities. **Missing:** snapshot comparison |
| 14 Reporting | Mostly done | Text, JSON, DOT, Mermaid, bundles, redaction. **Missing:** evidence timeline, snapshot diff |
| 15 Security | Partial | Read-only defaults, fixed command allowlist, redaction, archtest, Dependabot. **Missing:** written threat model, govulncheck/SAST in CI |
| 16 Tests | Partial | Unit, golden, fake cluster, netns, real-kernel eBPF, live KubeVirt lab (43/43). **Missing:** DNS, blackhole route, migration, performance suite beyond overhead |
| 17 CI/CD | Mostly done | Lint, tests, BPF build, kernel tests, cross-compile, tag-driven release. **Missing:** vulnerability scanning, reproducibility check |
| 18 Docs | Mostly done | README, CLI, eBPF, findings, CNI support, agents, lab. **Missing:** threat model, compatibility matrix page |

### Acceptance scenarios (directive section 19)

| Scenario | Status | Evidence |
| --- | --- | --- |
| A VLAN mismatch | Validated live | Lab C, E |
| B OVS forwarding problem | Partially validated | OVS re-tag live (lab E); missing OVS port by unit/golden only |
| C MTU | Validated live | Lab H |
| D Kernel drop | Validated live | Lab I: DirectlyAttributed drops on a real launcher pod |
| E TCP retransmission | Validated in netns | Guest TCP is not observable from the host (documented) |
| F LACP inconsistency | Unit and golden only | veth bonds cannot negotiate LACP; needs a real NIC or OVS LACP lab |
| G Live migration | Not validated | Needs a two-node lab |
| H Unsupported environment | Validated | Unprivileged run; hooks missing from the kernel are reported |
| I DNS failure | Not started | Needs `probe` and UDP/DNS tracing |
| J Multi-interface VM | Validated live | Lab VMs with pod and secondary networks |

### Outstanding regression

- **OVS false-port fix on the owner's cluster:** not yet re-run on the original cluster
  (older OVS CNI without `contPodUid`). The lab covers the newer OVS CNI only.

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
| eBPF tracer: drops with kernel drop reason and location | Done | `internal/ebpf` real-kernel test `TestTraceUDPClosedPortDrop`; non-drop reasons resolved from BTF by name (`TestNonDropReasonsAcrossKernels`) so 5.17-6.0 kernels keep NO_SOCKET drops |
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
| Live validation against a KubeVirt virt-launcher pod | Done | Lab run 2026-11-20 (see "Live lab validation") |

## Live lab validation

The lab is in `hack/lab` on branch `test/kubevirt-lab`. It ran on a GCE n2-standard-8 with nested virtualization:

| Component | Version |
| --- | --- |
| Kubernetes | v1.33.13 (kubeadm) |
| KubeVirt | v1.10.0 |
| cluster-network-addons | v0.104.0-rc-0 (Multus, Linux bridge CNI, OVS CNI) |
| containerd | 2.x |
| Kernel | 7.0.0-1017-gcp |

Lab topology:

- three CirrOS VMs: masquerade, Linux bridge VLAN 100, OVS VLAN 200;
- app pods on both VLANs;
- a bonded VLAN trunk and a simulated upstream switch.

**Result: 43 passed, 0 failed.**

| Scenario | Result |
| --- | --- |
| Healthy baseline (3 VMs) | No Error findings; `GUEST_IP_UNAVAILABLE` is correct, because CirrOS has no guest agent |
| `capabilities`, `host capabilities` | Pass; tracing Available (verifier load) |
| `topology -o dot`, `-o mermaid` | Pass |
| Missing NAD | `NAD_NOT_FOUND` |
| Uplink no longer carries VLAN 100 | `VLAN_MISMATCH` |
| VLAN filtering off | `VLAN_NOT_ENFORCED` |
| OVS port re-tagged | `VLAN_MISMATCH` |
| Bridge down | `HOST_INTERFACE_DOWN`; the snapshot state matched the kernel both down and up |
| Bond member link down | `BOND_MEMBER_DETACHED` (this needed the fix described below) |
| Bond member removed | No guess without declared members; `BOND_MEMBER_DETACHED` with expectations |
| MTU expectation | `MTU_MISMATCH` |
| Report bundle with `--redact` | Written; no VM addresses left |
| Every fault reverted | Baseline clean again |

Kernel trace of a live virt-launcher pod (scenario I):

- The launcher network namespace was found from the kubelet pod cgroup.
- Packets were counted on KubeVirt's interfaces: `tap…`, `k6t-…` and `…-nic`.
- All 7 hooks attached.
- Drops were attributed DirectlyAttributed.
- `diagnose --trace` reported `TRACE_DROPS`.
- Measured overhead: about 22k program runs, about 480 ns per run, 0.053% of one core.

Bugs and gaps found by the lab:

- **netdoctor bug, fixed in commit 119b670.** A bond member whose own link is down was not reported, because only the bond driver's MII status was checked. That status lags by up to miimon, and never changes with miimon=0.
- **Lab setup fixes, now in the scripts:**
  - KubeVirt's CRD needs server-side apply.
  - KubeVirt v1.10 container disks need containerd 2.1 or newer.
  - On Kubernetes 1.33 they also need the `ImageVolume` feature gate.
- **Open: scenario J is informational and recorded no drops.** Its traffic (app pod to gateway) does not involve the VM, so the VM's filter correctly excluded it. A VM-originated fault trace is still to be added.

## CLI contract

| Feature | Status | Evidence |
| --- | --- | --- |
| `--fail-on none|info|warning|error` on inspect/diagnose/report; exit 0 ok, 1 command error, 2 findings at threshold (report still written) | Done | `TestFailOnCLI`, `TestCheckFailOn` |
| `report vm/x [-o json] [--bundle F.tar.gz] [--redact]`: diagnosis, topology (JSON/text/DOT/Mermaid), capabilities, inputs, SHA-256 manifest; deterministic archive; consistent IP/MAC redaction (names are not redacted) | Done | `TestReportBundleRedacted`, `TestReportJSON`, `internal/bundle` tests |

## Documentation

| Item | Status |
| --- | --- |
| CLI reference with exit codes and file formats | Done (`docs/cli.md`) |
| eBPF internals: hooks, portability, attribution, overhead, tests | Done (`docs/ebpf.md`) |
| README: new commands, tracer security model, limitations | Done |

## Agent extensibility (design only)

| Item | Status | Evidence |
| --- | --- | --- |
| Extension points for diagnostic/remediation agents documented | Done | `docs/architecture-agents.md` |
| Engine read-only / no remediation dependency enforced | Done | `internal/archtest` (verified to fail on a planted write call) |
| Agents, remediation actions, policy, approval, audit | Not started by decision | Deferred by the owner; no code exists |

## Not started

| Feature | Notes |
| --- | --- |
| UDP/DNS, conntrack/routing, QEMU/vhost, OVS datapath hooks | Next eBPF hook groups |
| Opt-in connectivity `probe` | Must be explicit and labeled with its origin |
| OVN-Kubernetes / Kube-OVN adapters | Design in PR #7 |
| Docs: per-finding reference for TRACE_* in docs/findings.md | |

## Resuming

`git checkout feature/kernel-observability && PATH=$HOME/go/bin:$PATH make verify && make bpf-test` (root).
