# Capability audit and gap analysis

This document records the state of netdoctor before the `feature/network-diagnostics`
work, compares it with existing tools, and lists what that work adds. It is kept in the
repository so reviewers can check that new code fills real gaps rather than duplicating
existing tools.

## 1. Audit of v0.1 (commit `7ee850a`)

| Area | Status in v0.1 | Notes |
| --- | --- | --- |
| VM / VMI discovery (spec, phase, node, conditions, guest agent) | Implemented | Dynamic client, tolerant decoding of a field subset. |
| virt-launcher pod discovery | Implemented | `kubevirt.io/created-by` label **and** controller owner reference; `activePods` and node used to resolve migrations. |
| Multus `networks` / `network-status` annotations | Implemented | JSON and short form; namespace-qualified and bare names. |
| NAD lookup and CNI config parsing | Implemented | Single plugin and conflist; plugin roles; bridge/VLAN/master/MTU/IPAM type. |
| Per-network correlation (spec → guest → pod request → pod attachment → NAD) | Implemented | Interface-name matching with order fallback; the *method* used was not recorded. |
| Binding types | Partial | Detected (`bridge`, `masquerade`, `sriov`, `passt`, plugins) but not used to explain the data path. |
| IPAM details (subnets, gateways, routes) | Missing | Only the IPAM `type` was parsed. |
| VLAN trunk ranges | Partial | Only "trunk present" was recorded, not the VLAN IDs. |
| Topology as a graph with observed vs inferred relationships | Missing | `inspect` printed per-network layers only. |
| Host network state (interfaces, bridges, OVS, bonds, routes) | Missing | Nothing below the Kubernetes API. |
| OVS inspection | Missing | |
| Destination / route analysis | Missing | |
| Findings engine | Implemented | ID, severity, category (confidence), resource, summary, evidence, next action. No `expected` field. |
| Rules | Implemented for API-level checks | NAD missing/invalid, VMI not running, launcher pod missing/ambiguous, Multus attachment missing, interface spec/status mismatch, guest IP unavailable, RBAC/API availability. |
| Output | Implemented | Text and versioned JSON (`netdoctor/v1alpha1`). |
| Tests | Implemented | Unit, fake-client, RBAC-failure and golden tests with synthetic fixtures. |

## 2. Comparison with existing tools

| Tool | What it does well | Relationship to netdoctor |
| --- | --- | --- |
| `virtctl` (KubeVirt) | VM lifecycle, console, `guestosinfo`, port-forward | No network correlation or diagnostics. netdoctor complements it. |
| kubernetes-nmstate `NodeNetworkState` | Publishes each node's host network state as a cluster-scoped, read-only API object (`nodenetworkstates`, `status.currentState`) collected by an already-deployed handler | **Should be reused rather than duplicated**, because it needs no node access. Planned as a follow-up: the nmstate interface schema could not be verified against its documentation while this work was done, and netdoctor does not parse schemas it has not verified. |
| `ovs-vsctl`, `ovs-appctl`, `ovs-ofctl` | Authoritative OVS configuration, bond/LACP status, flows | netdoctor's host collector invokes a fixed set of their read-only subcommands and turns the output into structured evidence correlated with the VM. |
| iproute2 (`ip -j`, `bridge -j`) | Authoritative Linux link, address, route, bond and bridge-VLAN state | Same: fixed, read-only invocations with JSON output. |
| `ovnkube-trace` / `ovn-trace` (OVN-Kubernetes), `kubectl ko trace` (Kube-OVN) | Logical packet tracing for one CNI | CNI-specific. netdoctor does not reimplement tracing; it points to these where applicable. |
| Cilium CLI / Hubble, Calico `calicoctl` | CNI-specific connectivity tests and flow observability | Same: CNI-specific, out of scope. |
| `netshoot`, `kubectl debug` | Ad-hoc troubleshooting shells | Unstructured and manual. netdoctor's collector is the structured, read-only subset of what an engineer would run by hand. |
| `must-gather` / sos reports | Bulk collection for support cases | Collect everything, correlate nothing. netdoctor correlates a single VM's path. |

The gap netdoctor fills: **KubeVirt-centric correlation** from a VM interface through the
launcher pod, Multus and the NAD down to the host bridge, OVS port, VLAN, bond and route,
with every claim labeled by how it was established.

## 3. Gaps addressed by `feature/network-diagnostics`

| Gap | Delivered |
| --- | --- |
| Topology graph with provenance | `kubectl netdoctor topology` builds nodes and edges; every edge is labeled `Observed`, `Inferred` or `Declared`. |
| Correlation method transparency | Each pod attachment records how it was matched (`podInterfaceName`, request interface, default flag, or order of occurrence). |
| Binding semantics | Binding types explain the guest-to-pod relationship (inferred edges). |
| IPAM details and VLAN trunks | Subnets, gateways, routes and static addresses from `host-local`, `static` and `whereabouts`; trunk VLAN ranges for Linux bridge and OVS CNI. |
| Host data | Opt-in host snapshot produced by `kubectl netdoctor host collect`, run explicitly by an operator on the node, and passed to `topology`/`diagnose` with `--host-snapshot`. |
| OVS | Bridges, ports, interfaces, external IDs, tags, trunks, bonds, LACP status and per-bridge flow summaries (collector only). |
| Destination analysis | `kubectl netdoctor diagnose --destination` performs configuration and route analysis only; it never claims connectivity was tested. |
| Host and path rules | Missing host interface, OVS port missing / on the wrong bridge, VLAN and MTU mismatches, detached bond members, LACP partner disagreement, missing routes, incomplete topology. |
| Findings | `expected` field added; existing fields and IDs unchanged. |

## 4. Deliberately not addressed

- Live packet probes, packet capture or OpenFlow tracing. Opt-in eBPF tracing is
  proposed in [proposal 0001](proposals/0001-ebpf-trace.md).
- Automatic deployment of any privileged component (DaemonSet, debug pod).
- Inspection inside pod or guest network namespaces.
- `NodeNetworkState` as a host data source (follow-up, see section 2).
- Physical switch state. LACP partner identity is reported only as observed by the host,
  and compared only with partner IDs from other members of the same bond or with
  expectations supplied explicitly by the user.
