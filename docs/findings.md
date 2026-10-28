# Diagnostic findings reference

Every finding has a stable **ID**, a **severity** (`Error`, `Warning`, `Info`) and a
**category** that states how certain it is:

| Category | Meaning |
| --- | --- |
| `Confirmed` | The Kubernetes API state directly establishes the fact (for example, a referenced object does not exist). |
| `Suspected` | The state is consistent with a problem but has plausible benign explanations (timing, migrations, version differences). |
| `Unavailable` | netdoctor could not observe something (RBAC denial, API not served, missing annotation, no guest agent). Related checks were skipped rather than guessed. |

netdoctor v0.1 performs no data-plane tests. The absence of findings means only that
the inspected API state is consistent; it does **not** mean the network works.

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

## Correlation model

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
