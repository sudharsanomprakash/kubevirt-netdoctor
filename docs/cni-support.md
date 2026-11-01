# CNI support matrix

netdoctor works with any CNI at the Kubernetes API level, and models the host data
path for a growing set of plugins. This page states what is implemented today. Planned
work is listed separately and links to proposals.

## Support levels

| Level | What netdoctor does |
| --- | --- |
| **Host path modeled** | API checks, plus host snapshot correlation: the host device the NAD declares, uplinks, VLAN sub-interfaces, Linux/OVS bonds and LACP, VLAN and MTU checks. |
| **API level** | VM/VMI, launcher pod, Multus request and network-status correlation, NAD existence and CNI config parsing (plugin chain, VLAN, trunks, MTU, IPAM subnets/routes), interface spec/status checks and route analysis from declared IPAM. The host data path is reported as **not modeled**, so netdoctor makes no host-level claims, true or false. |
| **Unrecognized** | Any other plugin type is accepted, parsed generically and labeled "unrecognized plugin type". API-level checks still apply. |

## Secondary networks (Multus NADs)

| CNI plugin (`type`) | Level | Host checks implemented | Validated on |
| --- | --- | --- | --- |
| Linux bridge (`bridge`, `cnv-bridge`) | Host path modeled | Bridge exists and is up; VLAN filtering enabled when the NAD declares VLANs; uplinks carry the VLAN (bridge VLAN table); MTU; bonds/LACP. The pod's host veth is not identified (needs the pod network namespace). | Synthetic fixtures; real iproute2 output in network namespaces |
| OVS CNI (`ovs`) | Host path modeled | Bridge exists; the attachment's OVS port is found by OVS CNI `external_ids` (exact with `contPodUid`, inferred by `contIface` with older OVS CNI); port tag/trunks match the NAD; uplinks carry the VLAN; MTU; OVS bonds/LACP; secure fail mode without flows. | Synthetic fixtures; one live cluster |
| macvlan / ipvlan (`macvlan`, `ipvlan`) | Host path modeled | Parent (`master`) exists and is up; VLAN sub-interface and bond chain; MTU. | Synthetic fixtures |
| OVN-Kubernetes secondary (`ovn-k8s-cni-overlay`) | API level | Topology (layer2/layer3/localnet) is shown. No OVN or bridge-mapping checks yet: see [proposal 0002](proposals/0002-ovn-cni-support.md). | Parsing tests |
| Kube-OVN (`kube-ovn`) | API level | No Kube-OVN resource or OVN checks yet: see [proposal 0002](proposals/0002-ovn-cni-support.md). | Parsing tests |
| SR-IOV (`sriov`) | API level | NAD `resourceName` is shown. VF/PF state is not checked. | Parsing tests |
| host-device (`host-device`) | API level | | Parsing tests |
| ptp, Calico, Cilium, Flannel used as secondary plugins | API level | | Parsing tests |
| Meta plugins (`tuning`, `cnv-tuning`, `portmap`, `bandwidth`, `sbr`, `firewall`, `vrf`) | Recognized in chains | Chain order is checked (meta plugins should follow the interface plugin). | Parsing tests |

## Cluster (primary) network

The primary CNI (Calico, OVN-Kubernetes, Cilium, Flannel, Kube-OVN, ...) is treated
generically:

- The pod network attachment is correlated through Multus `network-status` when Multus
  is installed. Without Multus, only the VMI status side is available.
- The launcher pod's cluster interface is identified even when the VMI does not use the
  pod network.
- `diagnose --destination` uses KubeVirt binding semantics (masquerade, bridge, passt)
  for the default route and, with a host snapshot, the node's main routing table.
- CNI-specific internals are **not** modeled: Calico/Cilium policy and BGP state,
  OVN logical topology, and OVN-Kubernetes user-defined networks (UDN).

Validated on one live cluster with Calico as the primary CNI.

## Not supported

- Userspace and DPDK datapaths (OVS-DPDK, userspace CNI). Kernel tools cannot observe them.
- Guest-internal networking. The guest OS is not inspected.
- Physical switch state. Only what the host observes (for example LACP partner IDs) is reported.

## Planned

- OVN-Kubernetes localnet, then layer2/layer3 and UDN; Kube-OVN subnets, VLANs and
  provider networks: [proposal 0002](proposals/0002-ovn-cni-support.md).
- SR-IOV VF/PF state.
- Opt-in eBPF packet tracing, CNI-agnostic: [proposal 0001](proposals/0001-ebpf-trace.md).

Support for another CNI is a good first contribution: see
[CONTRIBUTING.md](../CONTRIBUTING.md) and open an issue describing the plugin's NAD
fields and where its host state lives.
