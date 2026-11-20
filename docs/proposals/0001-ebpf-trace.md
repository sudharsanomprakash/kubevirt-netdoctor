# Proposal 0001: Opt-in eBPF packet tracing for a VM network path

| | |
| --- | --- |
| Status | **Draft, not implemented** |
| Target | after v0.1.0 and live-cluster CI |
| Scope | `kubectl-netdoctor host trace` (experimental) and `diagnose --trace` |

## 1. Problem

Today netdoctor answers *"is the VM's network path configured consistently?"* It reads
the Kubernetes API and, from an operator-collected host snapshot, the node's bridges, OVS
ports, VLANs, bonds and routes. Every report states `Connectivity: NOT TESTED`, because
nothing observes traffic.

The questions operators ask next cannot be answered from configuration:

- Do the VM's packets reach its OVS port / Linux bridge port at all?
- Do they leave the node on the expected uplink, with the expected VLAN tag?
- If they disappear, **where** (which interface or kernel stage) and **why** (kernel
  drop reason)?

eBPF can observe exactly this inside the node's kernel, without changing configuration
and without sending traffic.

## 2. Goals

1. Show, for one VM interface and a short time window, which points of its host data
   path saw its packets: pod-side interface → host veth / OVS port → bridge → uplink.
2. Report kernel packet drops affecting those packets, with the kernel's drop reason
   where the kernel provides one.
3. Correlate the observations with the existing topology, so a finding can say, for
   example, "packets reached OVS port X but none left uplink Y during 30 s".
4. Keep the project's guarantees: opt-in, operator-run, read-only with respect to
   configuration, nothing auto-deployed, every claim labeled by how it was established.

## 3. Non-goals

- Continuous monitoring, metrics or dashboards. That is the job of existing tools such
  as Microsoft Retina, Hubble or Inspektor Gadget's long-running gadgets.
- Capturing packet payloads. Only header fields needed for matching are recorded.
- Generating test traffic. Tracing observes whatever traffic exists. Opt-in active probes
  are a separate roadmap item.
- Guest-internal visibility. The guest kernel is not instrumented.
- Proving end-to-end connectivity. A trace shows what one node saw; switch and remote
  hosts remain unobserved.

## 4. User experience

Same model as `host collect`: run on the VM's node, produce a file, analyze it from
where kubectl runs.

```sh
# On the node hosting the VM (root):
kubectl-netdoctor host trace \
  --snapshot /tmp/worker-2.json \          # from 'host collect', supplies interface names
  --vm-mac 02:00:00:00:c8:03 \             # or --vm-ip; the filter for all probes
  --duration 30s \                          # hard maximum enforced (e.g. 120s)
  --file /tmp/worker-2-trace.json

# Where kubectl runs (no sudo):
kubectl netdoctor diagnose vm/db-03 -n demo \
  --host-snapshot worker-2.json --trace worker-2-trace.json
```

Example of the intended text output (illustrative, not implemented):

```text
TRACE (node worker-2, 30s, filter mac 02:00:00:00:c8:03)
  veth3e9a1c7b (OVS port, pod side)   rx 412 pkts   tx 398 pkts      [Observed]
  br-data      (OVS datapath)         flows hit: NORMAL              [Observed]
  bond1/ens1f0 (uplink)               tx 0 pkts                      [Observed]
  drops: 412 x OVS_DROP_* reason (kernel) at ovs datapath            [Observed]

  ERROR   TRACE_EGRESS_MISSING [Observed] ...
          The VM's packets reached OVS port veth3e9a1c7b but none left an uplink of br-data
          during the 30 s window.
```

Trace-based findings get a distinct category, **`Observed`** (traffic seen during the
window). It is separate from `Confirmed` (configuration), so readers never confuse
"configured correctly" with "traffic flowed". Absence of packets is only reported when the
same window shows packets at an earlier point of the path. "No traffic at all" is
reported as inconclusive, never as a failure.

## 5. Design

### 5.1 What is observed

| Observation | Kernel mechanism (candidate) | Notes |
| --- | --- | --- |
| Packets per interface and direction for the VM's MAC/IP | tc (clsact) or tcx programs attached for the trace window only, counting matching packets and passing everything | Attach to: host-side veth / OVS pod port, bridge or uplink members from the snapshot. Programs must never drop or modify packets. |
| Kernel drops of matching packets, with reason | `skb:kfree_skb` tracepoint (drop reason available on newer kernels) | Filter in-kernel on the VM's MAC/IP to keep overhead low. |
| OVS datapath handling | kernel tracepoints/kprobes in the `openvswitch` module, and/or OVS's own `dpctl/dump-flows` before and after the window | `dpctl/dump-flows` is not eBPF but is cheap, read-only and shows which datapath flows the VM's traffic hits. |

The trace file records, per probe point: interface, direction, packet and byte counts,
first/last seen timestamps, and drop events (reason, location, count). It is a versioned
JSON schema like the host snapshot (`netdoctor.trace/v1alpha1`).

### 5.2 Implementation options

| Option | Pros | Cons |
| --- | --- | --- |
| **A. Embed small eBPF programs** via the `cilium/ebpf` Go library (CO-RE, BTF) | Self-contained static binary; fully auditable programs; filters exactly what we need; matches the "fixed, minimal" collector philosophy | Adds eBPF C sources and a `bpf2go` build step; kernel compatibility testing becomes our job |
| **B. Invoke an existing tool** (for example `pwru` for kernel path and drop tracing, or Inspektor Gadget's standalone `ig`) from an allowlist, like the OVS tools today | Little new code; mature, maintained tracers | Depends on external binaries being present and on their output formats; broader than needed; harder to guarantee bounds |
| **C. Depend on a full platform** (Retina, Hubble) | Rich observability | Requires a deployed agent (DaemonSet), contradicting the no-auto-deploy rule; overlaps with tools users can already run |

**Recommendation.** Validate the value with a short spike using option **B** on a lab
cluster. If it proves useful, implement option **A** with two or three small programs
(interface counters, filtered drop tracepoint), because the result is the most auditable
and needs nothing pre-installed. Option C is out of scope; the README can point to those
tools for continuous observability.

### 5.3 Correlation with the topology

The snapshot already identifies the VM's OVS port (by OVS CNI `external_ids`), the
bridge, uplinks, VLAN sub-interfaces and bond members. The tracer attaches only to
those interfaces. For Linux bridge attachments, where the host veth cannot be tied to the
pod from configuration alone, the MAC filter on the bridge ports identifies the VM's
traffic, and the matching veth is reported as **Inferred** from traffic.

## 6. Security and safety

| Requirement | How |
| --- | --- |
| Opt-in, operator-run | Separate subcommand run on the node; never deployed by netdoctor. |
| Minimum privilege | Needs `CAP_BPF`, `CAP_PERFMON` and `CAP_NET_ADMIN` (or root). Documented, checked up front, with a clear error if missing. |
| No configuration change | Programs only count and observe (tc programs return "pass"); nothing pinned; everything detached on exit, on timeout, on Ctrl-C and on error. |
| Bounded | Hard maximum duration; fixed-size maps; per-event rate limiting; filter in kernel on the VM's MAC/IP. |
| Privacy | No payloads. Only MAC/IP/VLAN/protocol fields needed for matching. Output file mode 0600. |
| No arbitrary input | Interfaces come from the snapshot (validated names); the filter is a parsed MAC/IP, never a user-supplied BPF program or expression. |
| Honest output | Trace findings use the `Observed` category and state the window; absence is reported only relative to an earlier point that saw packets. |

## 7. Requirements and compatibility

- Linux with BTF (`/sys/kernel/btf/vmlinux`) for CO-RE. Exact minimum kernel versions
  for tcx and for drop reasons on `kfree_skb` must be validated during the spike.
  Where a capability is missing, that probe is reported as unavailable instead of failing
  the whole trace.
- OVS kernel datapath. DPDK/userspace datapaths are not visible to kernel eBPF and are
  reported as unsupported.
- `amd64` and `arm64`.

## 8. Testing

- Unit tests for the trace file schema, correlation and rules, with synthetic traces
  (same approach as host snapshots).
- Integration test in disposable network namespaces: veth pairs and a bridge, generate
  traffic with a small Go sender, assert counts and one forced drop.
- Live-cluster validation on kind + KubeVirt + Multus + OVS before the feature leaves
  experimental status.

## 9. Phasing

1. **Spike (option B), lab only:** confirm that per-interface counts and drop reasons
   locate real faults: VLAN mismatch on the uplink, OVS port missing a flow, MTU drops.
2. **Trace file schema + `diagnose --trace` correlation and rules**, driven by synthetic
   traces.
3. **Embedded tracer (option A)** behind `host trace`, experimental.
4. Decide on graduation based on real-world feedback.

## 10. Open questions

- Minimum kernel versions to support, given the spread of distributions running KubeVirt.
- Whether to record a small number of sampled packet headers (for VLAN tag verification)
  or counts only.
- How to treat OVS hardware offload, where packets bypass the kernel datapath.
- Whether `diagnose` should accept traces from several nodes, so both ends of an
  east-west flow can be compared.
