# Kernel tracer (eBPF) internals

`kubectl-netdoctor host trace` loads netdoctor's BPF programs, records for a bounded
window, and detaches. This page explains what it hooks, how it filters and attributes
events, and how its cost and failure modes are bounded.

## Safety properties

- **Observation only.** Every program attaches to a passive BTF tracepoint (`tp_btf`)
  and returns without side effects. Nothing is attached to XDP, TC or socket hooks, so no
  packet can be modified, dropped, redirected or sent.
- **Explicit and time-bounded.** The tracer runs only when an operator starts it, for at
  most 10 minutes. Programs are detached on completion, on Ctrl-C and on error.
- **Filtered in the kernel.** Events outside the filter are discarded inside the BPF
  program. Node-wide tracing requires `--unfiltered`.
- **Bounded memory.** The ring buffer is 4 KiB–64 MiB (default 1 MiB). When it is full,
  events are dropped and counted (`lostEvents`). Counters are per-CPU and always exact.
- **Bounded output.** At most `--max-events` events are stored. Further events are
  counted (`suppressedEvents`), so totals stay correct.

## Hooks

| Group | Tracepoint | Event | Data |
| --- | --- | --- | --- |
| drops | `skb:kfree_skb` | `drop` | interface, netns, 5-tuple, length, kernel drop reason (from `enum skb_drop_reason` in the running kernel's BTF), drop location (`function+offset` via `/proc/kallsyms`) |
| tcp | `tcp:tcp_retransmit_skb` | `tcp_retransmit` | 5-tuple, smoothed RTT |
| tcp | `tcp:tcp_receive_reset` | `tcp_reset_received` | 5-tuple |
| tcp | `tcp:tcp_send_reset` | `tcp_reset_sent` | 5-tuple; from packet headers when no socket exists (closed port) |
| tcp | `sock:inet_sock_set_state` | `tcp_state` | 5-tuple, old and new state |
| interfaces | `net:netif_receive_skb`, `net:net_dev_queue` | counters only | packets and bytes per interface and direction that match the filter |

`kfree_skb` calls whose reason is `SKB_NOT_DROPPED_YET` or `SKB_CONSUMED` are skipped,
because they are normal frees rather than drops. The numeric values of these reasons
differ between kernels; on 5.17–6.0, for example, value 1 is `NO_SOCKET`. The tracer
therefore reads them by name from the running kernel's BTF, and on kernels without them
it records every `kfree_skb`.

## Portability

Programs are compiled once (`bpf2go`, `-target amd64,arm64`) and embedded in the binary.
Field accesses use CO-RE (`preserve_access_index`). At load time they are relocated
against the running kernel's BTF (`/sys/kernel/btf/vmlinux`), so no kernel headers are
needed on the node.

Before loading, the tracer checks each tracepoint's `btf_trace_<name>` type in the
kernel's BTF. A missing hook is skipped and reported as a limitation (and as
`TRACE_HOOK_UNAVAILABLE` in `diagnose`) instead of failing the whole trace.

`host capabilities` runs the same checks and also loads the programs through the
verifier without attaching them.

Requirements:

- Linux with BTF.
- Ring buffer support (5.8+).
- Drop reasons need 5.17+; on older kernels drops are reported without a reason.
- Privileges: CAP_BPF and CAP_PERFMON, or root.
- Measured overhead needs CAP_SYS_ADMIN for `BPF_ENABLE_STATS`.

## Filtering and attribution

The filter is a set of `(netns inode, ifindex)` pairs plus a set of IP addresses
(IPv4-mapped into 16 bytes). An event is kept when it matches either set. It records
which set it matched in `matchedBy`.

With `--plan`, the tracer resolves the VM's virt-launcher pod on the node:

1. It scans `/proc/*/cgroup` for the pod UID. Kubelet writes it with dashes (cgroupfs driver) or underscores (systemd driver).
2. It takes the processes' network namespace and ignores the host namespace.
3. It refuses to guess if the pod spans several namespaces.
4. It filters on every non-loopback interface in that namespace, plus the VM addresses from the plan.

Each event is then classified:

| Attribution | Rule |
| --- | --- |
| DirectlyAttributed | Matched an interface inside the launcher pod's network namespace |
| Correlated | Matched an address that belongs only to this VM (IPAM-allocated) |
| Ambiguous | Matched only addresses other workloads can share: KubeVirt's masquerade range 10.0.2.0/24 and fd10:0:2::/120, and link-local addresses |
| Unattributed | Node-wide trace |

`diagnose --trace` counts only DirectlyAttributed and Correlated events against the VM:

- A finding is Confirmed when at least one directly attributed event supports it, and Suspected otherwise.
- The trace is rejected (`TRACE_TARGET_MISMATCH`) when its pod UID is not the VM's current launcher pod, for example after a restart or migration.

## Overhead

With CAP_SYS_ADMIN, the tracer enables BPF run-time statistics for the window and
reports, per program and in total:

- run count;
- CPU nanoseconds;
- average nanoseconds per run;
- CPU time as a percentage of one core over the window.

These are kernel measurements, not estimates. On the development VM the end-to-end test
measured about 0.008% of one core at roughly 1,200 ns per run.

## Tests

`make bpf-test` (root) loads the real programs and generates traffic between network
namespaces:

| Test | What it proves |
| --- | --- |
| `TestTraceUDPClosedPortDrop` | UDP to a closed port gives a `NO_SOCKET` drop on the right interface and netns, with rx counters and overhead measured |
| `TestTraceTCPResetToClosedPort` | Reset sent with a NULL socket, reset received, `SYN_SENT` transition |
| `TestTraceTCPRetransmitWhenPeerLinkDown` | Retransmissions after the peer link goes down; counter equals the number of events |
| `TestTraceLostEventsAreCounted` | With a 4 KiB ring buffer, recorded + suppressed + lost equals the kernel drop counter |
| `TestTraceFilterExcludesOtherTraffic` | Non-matching traffic is not recorded |
| `TestTraceEndToEnd` (internal/cli) | `trace` → `host trace --plan` → `diagnose --trace`, with a simulated launcher pod (its own netns plus a kubelet-style pod cgroup) |

CI runs these as root on GitHub-hosted runners. CI also compiles the BPF source with
`-Werror` and checks that the generated Go bindings match it.

## Not yet covered

The following hook groups are not implemented yet:

- UDP/DNS latency and failures;
- conntrack and routing decisions;
- QEMU/vhost and tap queues;
- the OVS kernel datapath.

The tracer has not yet been validated against a live KubeVirt virt-launcher pod.
