# Proposal 0002: OVN-Kubernetes and Kube-OVN support

| | |
| --- | --- |
| Status | **Draft, not implemented** |
| Target | after v0.1.0 |
| Scope | Host and API correlation for OVN-based CNIs |

## 1. Background

OpenShift (and OpenShift Virtualization) uses **OVN-Kubernetes**. **Kube-OVN** is a
separate CNCF project that is also built on OVN and is used with KubeVirt outside
OpenShift. Today netdoctor recognizes both plugin types in NADs
(`ovn-k8s-cni-overlay`, `kube-ovn`) but models neither host data path. It reports
them as "not modeled" (see the [CNI support matrix](../cni-support.md)).

The goal is the same evidence-based approach used for OVS CNI and Linux bridge:
correlate declared configuration with observed host and API state, label every
relationship by provenance, and never invent expected values.

## 2. Verification status of the facts this design relies on

Field and annotation names were checked against upstream source before writing this
proposal. Items marked **To verify** must be confirmed, ideally on a lab cluster, before
code depends on them.

| Fact | Status | Source |
| --- | --- | --- |
| OVN-K NAD keys: `topology`, `netAttachDefName`, `subnets` (comma-separated), `excludeSubnets`, `mtu`, `vlanID`, `physicalNetworkName` (localnet; defaults to the NAD `name`), `allowPersistentIPs`, `role` | Verified | `go-controller/pkg/cni/types/types.go` |
| OVN-K topology values `layer3`, `layer2`, `localnet` | Verified (docs) | `docs/features/multiple-networks/multi-homing.md` |
| OVN-K pod annotation `k8s.ovn.org/pod-networks` with `ip_addresses`, `mac_address`, `gateway_ips`, `routes` (`dest`, `nextHop`), `role` (`primary`, `secondary`, `infrastructure-locked`) | Verified | `go-controller/pkg/util/pod_annotation.go` |
| OVN-K pod port on `br-int` with Interface `external_ids` `iface-id`, `iface-id-ver` (pod UID), `sandbox`, `attached_mac`, optionally `pod-if-name`, `ip_addresses` | Verified | `go-controller/pkg/cni/helper_linux.go` |
| `iface-id` format (from `GetIfaceId` / `GetUDNIfaceId`) and the secondary-network external ID keys (`NetworkExternalID`, `NADExternalID` values) | **To verify** | constants not fetched |
| Localnet bridge mapping via Open_vSwitch `external_ids:ovn-bridge-mappings` (`physnet:bridge`) | **To verify**. Docs say "ovn-bridge-mappings is configured appropriately on the chassis" but do not state the exact location or format. | `multi-homing.md` |
| UDN API `k8s.ovn.org/v1` `UserDefinedNetwork` / `ClusterUserDefinedNetwork` | Verified (docs only) | `docs/features/user-defined-networks/user-defined-networks.md` |
| Kube-OVN annotations `ovn.kubernetes.io/ip_address`, `mac_address`, `logical_switch`, `allocated`, `routed`; per-provider templates `%s.kubernetes.io/<key>` | Verified (what fills `%s` is **to verify**) | `pkg/util/const.go` |
| Kube-OVN Subnet (`cidrBlock`, `gateway`, `provider`, `vlan`, `excludeIps`), Vlan (`id`, `provider`), ProviderNetwork (`defaultInterface`, `customInterfaces`, `nodeSelector`, `excludeNodes`, ...), all cluster-scoped | Verified | `pkg/apis/kubeovn/v1/{subnet,vlan,provider-network}.go` |
| Kube-OVN API group `kubeovn.io/v1` | **To verify** | not in the fetched files |
| Kube-OVN NAD attachment fields (for example a `provider` key) | **To verify** | docs site unreachable during research |

## 3. Phase 1: OVN-Kubernetes localnet (highest value)

Localnet is how OpenShift VMs commonly reach physical networks and VLANs, and its
failures look like those netdoctor already diagnoses: wrong VLAN, missing mapping,
uplink not carrying the VLAN.

**API side**
- Parse `topology`, `physicalNetworkName` (falling back to `name`), `vlanID`, `subnets`
  and `mtu` from the NAD. Subnets feed the existing route analysis.
- Read `k8s.ovn.org/pod-networks` on the launcher pod to get the per-network IP, MAC and
  gateway as OVN-K assigned them (Observed).

**Host side** (extends `host collect`, still read-only and allowlisted)
- Collect the bridge mappings (exact source **to verify**, expected Open_vSwitch
  `external_ids:ovn-bridge-mappings`).
- Resolve physical network → OVS bridge → uplinks, reusing the existing OVS uplink,
  bond and LACP correlation.
- Identify the VM's port on `br-int` by Interface `external_ids:iface-id-ver` (pod UID)
  plus `iface-id`. This is exact when the pod UID matches, using the same tiering as
  OVS CNI.

**New findings (draft)**

| ID | When |
| --- | --- |
| `OVNK_BRIDGE_MAPPING_MISSING` | No mapping on the node for the NAD's physical network (Confirmed when the mapping source was collected). |
| `OVNK_PORT_MISSING` | No `br-int` port carries the launcher pod's UID, while the attachment is reported. |
| `VLAN_MISMATCH` (reused) | The mapped bridge's uplinks do not carry the NAD's `vlanID`. |
| `MTU_MISMATCH` (reused) | Devices on the mapped bridge path are below the NAD MTU. |

## 4. Phase 2: OVN-Kubernetes overlays and user-defined networks

- **Layer2 / layer3 secondary networks and UDN.** Read `UserDefinedNetwork` /
  `ClusterUserDefinedNetwork` (API, read-only) and correlate them with NADs, the pod
  annotation's `role` (primary vs secondary) and the `br-int` port.
- **OVN logical state** (logical switch port exists, `up`, bound to this node's chassis)
  needs read access to the OVN northbound/southbound databases, which run inside
  OVN-K pods. This is an explicit, opt-in collector step with its own documented
  permissions, never automatic. Until then, overlays stay "API level" with the pod
  annotation and `br-int` port as the only evidence.

## 5. Phase 3: Kube-OVN

Kube-OVN keeps most of its intent in cluster-scoped resources, so much of it is
API-only and needs no node access:

- Pod annotations (`ovn.kubernetes.io/ip_address`, `logical_switch`, `allocated`, and the
  per-provider variants) → which Subnet the VM is on and its address.
- Subnet → `cidrBlock`, `gateway`, `vlan`, `provider`. Vlan → `id`, `provider`.
  ProviderNetwork → node interface (`defaultInterface`, `customInterfaces`, node
  selection).
- Host side: the provider network's interface and OVS bridge on the VM's node, reusing
  the existing uplink, VLAN, MTU and bond checks.

Draft findings: the subnet the annotation names does not exist; VLAN referenced by
the Subnet missing; provider network not applicable to the VM's node; provider interface
missing or down on the node; and the reused VLAN/MTU/bond findings.

RBAC additions (all `get`/`list`, documented): `k8s.ovn.org` UDN/CUDN for phase 2, and
the Kube-OVN group (to verify) `subnets`, `vlans`, `provider-networks` for phase 3.

## 6. Principles carried over

- Read-only; nothing deployed; host data only from the operator-run collector.
- Every relationship labeled Observed / Declared / Inferred; ambiguous matches reported
  as gaps, not guesses.
- Expected values only from the NAD, the CNI's own resources, or the user's
  expectations file.
- Synthetic fixtures and golden tests for each finding; live validation on an
  OpenShift/OVN-K cluster and a Kube-OVN cluster before leaving experimental status.

## 7. Open questions

- The exact bridge-mapping source and format, and whether OpenShift exposes it
  differently (for example through NMState-managed configuration).
- The `iface-id` format for UDN/secondary networks, needed to match ports without
  relying on the pod UID alone.
- Whether reading OVN databases can be done safely from the node (local socket) versus
  through the OVN-K pods, and which is acceptable to OpenShift operators.
