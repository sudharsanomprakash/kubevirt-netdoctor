# Diagnostic findings reference

Every finding has a stable **ID**, a **severity** (`Error`, `Warning`, `Info`), a
**category** (the confidence classification), the affected **resource**, observed
**evidence**, the **expected** state when one is known (and where that expectation came
from), and a recommended **next action**.

| Category | Meaning |
| --- | --- |
| `Confirmed` | The observed state (Kubernetes API or host snapshot) directly establishes the fact, for example a referenced object does not exist or an OVS port has a different tag than the NAD declares. |
| `Suspected` | The state is consistent with a problem but has plausible benign explanations (timing, migrations, version differences, unobservable guest configuration). |
| `Unavailable` | netdoctor could not observe something (RBAC denial, API not served, missing annotation, no guest agent, host source not collected). Related checks were skipped rather than guessed. |

Expected values come only from declared configuration (VMI spec, NAD config) or from an
expectations file the user supplies (`diagnose --expectations`, see
[`examples/expectations.yaml`](../examples/expectations.yaml)). LACP partner IDs are
compared only between members of one bond or with a declared partner.

netdoctor performs no data-plane tests. The absence of findings means only that the
inspected state is consistent; it does **not** mean the network works.

## Findings

| ID | Severity | Category | Raised when |
| --- | --- | --- | --- |
| `ACCESS_DENIED` | Warning | Unavailable | A `get`/`list` was rejected (HTTP 401/403). Dependent checks are skipped. |
| `API_UNAVAILABLE` | Warning | Unavailable | An API group is not served (e.g. Multus NAD CRD absent) or a request failed for non-RBAC reasons. |
| `VMI_NOT_RUNNING` | Info / Warning / Error | Confirmed / Suspected | No VMI exists (stopped VM: Info; VM expected to run: Warning) or the VMI phase is not `Running` (Pending/Scheduling/Scheduled/Unknown: Warning, Failed: Error, Succeeded: Info). |
| `NAD_NOT_FOUND` | Error | Confirmed | A NetworkAttachmentDefinition referenced by `spec.networks[].multus.networkName` does not exist. |
| `NAD_REFERENCE_INVALID` | Error | Confirmed | `multus.networkName` is not `<name>` or `<namespace>/<name>`. |
| `NAD_CONFIG_INVALID` | Error | Confirmed | `spec.config` is not valid JSON or not a valid CNI config/conflist (missing `type`, empty `plugins`, VLAN out of range, wrongly typed fields). |
| `NAD_CONFIG_SUSPICIOUS` | Warning | Suspected | The config parses but has warnings (no `cniVersion`, meta plugin first, no interface-creating plugin, ...). |
| `NAD_CONFIG_NOT_INLINE` | Info | Unavailable | `spec.config` is empty; Multus reads a node-local file that the API does not expose. |
| `LAUNCHER_POD_MISSING` | Info / Error | Suspected | No active virt-launcher pod (matched by `kubevirt.io/created-by` label **and** controller owner reference) exists. Error when the VMI is `Scheduled`/`Running`. |
| `LAUNCHER_POD_AMBIGUOUS` | Warning | Suspected | Several active launcher pods exist and none is uniquely on the VMI's node. Attachment checks are skipped. |
| `LAUNCHER_POD_MULTIPLE` | Info | Confirmed | Several active launcher pods exist (typically live migration); the pod on the VMI node was inspected. |
| `LAUNCHER_NODE_MISMATCH` | Warning | Suspected | The selected launcher pod is on a different node than VMI `status.nodeName`. |
| `MULTUS_ANNOTATION_INVALID` | Warning | Suspected | The launcher pod's `k8s.v1.cni.cncf.io/networks` or `network-status` annotation cannot be parsed. |
| `MULTUS_REQUEST_MISSING` | Warning | Suspected | A VMI Multus network has no matching entry in the launcher pod's `networks` annotation. |
| `MULTUS_ATTACHMENT_MISSING` | Error / Warning | Confirmed / Suspected | The running launcher pod's `network-status` does not list a requested secondary attachment (Error, Confirmed), or lists no default network for a pod network (Warning, Suspected). |
| `MULTUS_STATUS_UNAVAILABLE` | Info | Unavailable | Multus networks are used but the launcher pod has no `network-status` annotation. |
| `INTERFACE_STATUS_MISMATCH` | Warning / Error | Confirmed / Suspected | For a running VMI: a spec interface is missing from status, status reports an interface absent from the spec, the reported MAC differs from `spec.macAddress`, or a network has no interface of the same name. |
| `GUEST_IP_UNAVAILABLE` | Info | Unavailable | Interfaces report no IP and the guest agent is not connected, so in-guest addresses cannot be known. |
| `MULTUS_ATTACHMENT_MISMATCH` | Warning | Suspected | The MAC address KubeVirt requested through Multus differs from the one network-status reports (the CNI chain may not support the `mac` capability). |

### Host and OVS findings (require `--host-snapshot`, experimental)

| ID | Severity | Category | Raised when |
| --- | --- | --- | --- |
| `HOST_SNAPSHOT_NODE_MISMATCH` | Warning | Unavailable | The snapshot's node is not the VMI's node; host checks are skipped. |
| `HOST_SNAPSHOT_HOSTNAME_MISMATCH` | Warning | Suspected | The snapshot's hostname does not agree with its node name (collected with `--allow-node-name-mismatch`); host findings may describe another machine. |
| `HOST_COLLECTOR_UNAVAILABLE` | Warning | Unavailable | A snapshot source failed (typically for lack of privileges); dependent checks are skipped. |
| `HOST_INTERFACE_MISSING` | Error | Confirmed | The bridge, OVS bridge or parent interface a NAD declares does not exist on the node (or OVS is not installed for an OVS NAD). |
| `HOST_INTERFACE_DOWN` | Error / Warning | Confirmed | The declared host device is down (Error), or some (Warning) or all (Error) of its uplinks are down. |
| `OVS_PORT_MISSING` | Error / Warning | Confirmed / Suspected | No OVS port created by OVS CNI exists for the attachment. Confirmed (Error) only when the node's OVS CNI records pod UIDs (`external_ids:contPodUid`) and Multus reports the attachment. With OVS CNI versions that do not record pod UIDs the port is matched by `contIface`, which KubeVirt derives from the network name, so the result is Suspected; if several ports match, the port is reported as unidentifiable (a topology gap) instead. |
| `OVS_PORT_WRONG_BRIDGE` | Error | Confirmed | The attachment's OVS port is on a different bridge than the NAD declares. |
| `OVS_BRIDGE_NO_FLOWS` | Warning | Confirmed | A bridge on the path is in `secure` fail mode and has no OpenFlow flows. |
| `VLAN_MISMATCH` | Error | Confirmed | The OVS port tag or trunks differ from the NAD; no uplink of the bridge carries the VLAN (OVS trunks/tag or Linux bridge VLAN table); or the NAD's VLAN differs from the expectations file. |
| `VLAN_NOT_ENFORCED` | Warning | Confirmed | A NAD declares VLANs on a Linux bridge whose VLAN filtering is disabled (the bridge CNI plugin enables filtering only when it creates the bridge). |
| `MTU_MISMATCH` | Error | Confirmed | A device on the path (bridge, OVS pod port, uplink, VLAN parent, bond member) has an MTU below the NAD's or expected MTU; or the NAD MTU differs from the expectations file. |
| `BOND_MEMBER_DETACHED` | Warning / Error | Confirmed | A bond member on the path is down, disabled, LACP-detached/defaulted/expired, or in a different 802.3ad aggregator (Error when all members are); or an expected member is missing. |
| `LACP_PARTNER_MISMATCH` | Error | Confirmed | Members of one bond report different LACP partner system IDs, or a partner differs from the expectations file. |

### Destination findings (require `--destination`)

| ID | Severity | Category | Raised when |
| --- | --- | --- | --- |
| `ROUTE_MISSING` | Warning | Suspected | No declared IPAM subnet or route covers the destination and no pod-network binding provides a default route. The guest's own routes are not observable. |
| `ROUTE_UNDETERMINED` | Info | Unavailable | No candidate exists, but some VM networks have guest-configured addressing (no NAD IPAM). KubeVirt reports guest IPs without prefix lengths, so their reachability cannot be evaluated. |
| `ROUTE_AMBIGUOUS` | Warning | Suspected | Equally specific candidates exist on several VM networks. |
| `ROUTE_GATEWAY_OFFLINK` | Error | Confirmed | The selected route's gateway is outside the network's declared subnets. |
| `HOST_ROUTE_MISSING` | Error | Confirmed | Pod-network traffic would be routed by the node, and its main routing table has no route to the destination. |
| `HOST_ROUTE_REJECTED` | Error | Confirmed | The node's best route is a blackhole, unreachable or prohibit route. |
| `HOST_ROUTE_EGRESS_DOWN` | Error | Confirmed | The node's route uses an interface that is down. |

### Coverage

| ID | Severity | Category | Raised when |
| --- | --- | --- | --- |
| `TOPOLOGY_INCOMPLETE` | Info | Unavailable | Parts of the path could not be observed (no launcher pod, no network-status, NAD config not inline, no host snapshot, OVS not collected, ...). Observed absences are reported by their own findings instead. |

## Correlation model

For each attachment, `topology` and the JSON report record how the Multus network-status
entry was matched (`matchedBy`): `podInterfaceName` (VMI status), `requestInterface`
(Multus request), `defaultFlag`, or `referenceOrder`. The last is an inference and is
shown as such.

For each VMI network netdoctor records, separately and without assuming they are the
same thing:

1. the **spec interface** (binding method, requested MAC),
2. the **guest interface** from VMI status (guest interface name, MAC, IPs, info source),
3. the **pod request** from the launcher pod's `k8s.v1.cni.cncf.io/networks` annotation,
4. the **pod attachment** reported by Multus in `k8s.v1.cni.cncf.io/network-status`,
5. the **NetworkAttachmentDefinition** and its parsed CNI plugin chain.

Pod-level entries are matched by pod interface name when known (VMI status
`podInterfaceName` or the Multus request), otherwise by NAD reference in order of
occurrence, so two networks using the same NAD are not conflated.
