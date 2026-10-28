// Package kubevirt reads VirtualMachine, VirtualMachineInstance and
// virt-launcher pod state from the Kubernetes API.
//
// The types below intentionally model only the subset of the kubevirt.io/v1
// API that netdoctor reads. Objects are fetched through the dynamic client and
// decoded tolerantly, so unknown or newer fields never break inspection and
// the tool is not pinned to a single KubeVirt release.
package kubevirt

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Well-known labels KubeVirt sets on virt-launcher pods.
const (
	// LabelCreatedBy holds the UID of the VMI that owns a launcher pod.
	LabelCreatedBy = "kubevirt.io/created-by"
	// LabelApp is "virt-launcher" on launcher pods.
	LabelApp = "kubevirt.io"
	// LauncherAppValue is the value of LabelApp on launcher pods.
	LauncherAppValue = "virt-launcher"
)

// VirtualMachine is the subset of kubevirt.io/v1 VirtualMachine read by netdoctor.
type VirtualMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VirtualMachineSpec   `json:"spec,omitempty"`
	Status            VirtualMachineStatus `json:"status,omitempty"`
}

// VirtualMachineSpec is the subset of the VM spec read by netdoctor.
type VirtualMachineSpec struct {
	// Running is deprecated upstream in favor of RunStrategy but still honored.
	Running     *bool        `json:"running,omitempty"`
	RunStrategy string       `json:"runStrategy,omitempty"`
	Template    *VMITemplate `json:"template,omitempty"`
}

// VMITemplate is the VM's embedded VMI template.
type VMITemplate struct {
	Spec VMISpec `json:"spec,omitempty"`
}

// VirtualMachineStatus is the subset of the VM status read by netdoctor.
type VirtualMachineStatus struct {
	PrintableStatus string      `json:"printableStatus,omitempty"`
	Ready           bool        `json:"ready,omitempty"`
	Created         bool        `json:"created,omitempty"`
	Conditions      []Condition `json:"conditions,omitempty"`
}

// Run strategies defined by KubeVirt.
const (
	RunStrategyAlways         = "Always"
	RunStrategyHalted         = "Halted"
	RunStrategyManual         = "Manual"
	RunStrategyRerunOnFailure = "RerunOnFailure"
	RunStrategyOnce           = "Once"
	RunStrategyWaitAsReceiver = "WaitAsReceiver"
)

// EffectiveRunStrategy resolves the run strategy, honoring the deprecated
// spec.running field. It returns "" when neither field is set.
func (vm *VirtualMachine) EffectiveRunStrategy() string {
	if vm.Spec.RunStrategy != "" {
		return vm.Spec.RunStrategy
	}
	if vm.Spec.Running != nil {
		if *vm.Spec.Running {
			return RunStrategyAlways
		}
		return RunStrategyHalted
	}
	return ""
}

// VirtualMachineInstance is the subset of kubevirt.io/v1 VirtualMachineInstance
// read by netdoctor.
type VirtualMachineInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VMISpec   `json:"spec,omitempty"`
	Status            VMIStatus `json:"status,omitempty"`
}

// VMISpec is the network-relevant subset of the VMI spec.
type VMISpec struct {
	Domain   DomainSpec `json:"domain,omitempty"`
	Networks []Network  `json:"networks,omitempty"`
}

// DomainSpec holds the VMI domain devices.
type DomainSpec struct {
	Devices Devices `json:"devices,omitempty"`
}

// Devices holds the VMI interfaces.
type Devices struct {
	Interfaces []Interface `json:"interfaces,omitempty"`
}

// Interface is a guest-facing interface definition. Exactly one binding
// method field is expected to be set; the set of methods has changed across
// KubeVirt releases, so bindings are kept as raw presence markers.
type Interface struct {
	Name       string           `json:"name"`
	MacAddress string           `json:"macAddress,omitempty"`
	Model      string           `json:"model,omitempty"`
	Bridge     *json.RawMessage `json:"bridge,omitempty"`
	Masquerade *json.RawMessage `json:"masquerade,omitempty"`
	SRIOV      *json.RawMessage `json:"sriov,omitempty"`
	Passt      *json.RawMessage `json:"passt,omitempty"`
	Slirp      *json.RawMessage `json:"slirp,omitempty"`
	Macvtap    *json.RawMessage `json:"macvtap,omitempty"`
	Binding    *PluginBinding   `json:"binding,omitempty"`
}

// PluginBinding is a network binding plugin reference.
type PluginBinding struct {
	Name string `json:"name"`
}

// BindingMethod returns a short name for the interface binding method, or
// "unknown" when no recognized method is set.
func (i Interface) BindingMethod() string {
	switch {
	case i.Binding != nil:
		return "plugin:" + i.Binding.Name
	case i.Bridge != nil:
		return "bridge"
	case i.Masquerade != nil:
		return "masquerade"
	case i.SRIOV != nil:
		return "sriov"
	case i.Passt != nil:
		return "passt"
	case i.Slirp != nil:
		return "slirp"
	case i.Macvtap != nil:
		return "macvtap"
	default:
		return "unknown"
	}
}

// Network is a VMI network source.
type Network struct {
	Name   string         `json:"name"`
	Pod    *PodNetwork    `json:"pod,omitempty"`
	Multus *MultusNetwork `json:"multus,omitempty"`
}

// PodNetwork selects the pod's primary (cluster default) network.
type PodNetwork struct {
	VMNetworkCIDR string `json:"vmNetworkCIDR,omitempty"`
}

// MultusNetwork references a NetworkAttachmentDefinition.
type MultusNetwork struct {
	NetworkName string `json:"networkName"`
	// Default marks this network as the pod's default network, replacing the
	// cluster default network.
	Default bool `json:"default,omitempty"`
}

// VMIStatus is the network-relevant subset of the VMI status.
type VMIStatus struct {
	Phase          string               `json:"phase,omitempty"`
	NodeName       string               `json:"nodeName,omitempty"`
	Reason         string               `json:"reason,omitempty"`
	Conditions     []Condition          `json:"conditions,omitempty"`
	Interfaces     []InterfaceStatus    `json:"interfaces,omitempty"`
	ActivePods     map[types.UID]string `json:"activePods,omitempty"`
	MigrationState *MigrationState      `json:"migrationState,omitempty"`
}

// VMI phases.
const (
	PhasePending    = "Pending"
	PhaseScheduling = "Scheduling"
	PhaseScheduled  = "Scheduled"
	PhaseRunning    = "Running"
	PhaseSucceeded  = "Succeeded"
	PhaseFailed     = "Failed"
	PhaseUnknown    = "Unknown"
)

// InterfaceStatus is an interface as reported in VMI status. Name refers to
// the spec interface/network name; InterfaceName is the name inside the guest.
type InterfaceStatus struct {
	Name             string   `json:"name,omitempty"`
	InterfaceName    string   `json:"interfaceName,omitempty"`
	IP               string   `json:"ipAddress,omitempty"`
	IPs              []string `json:"ipAddresses,omitempty"`
	MAC              string   `json:"mac,omitempty"`
	InfoSource       string   `json:"infoSource,omitempty"`
	PodInterfaceName string   `json:"podInterfaceName,omitempty"`
	LinkState        string   `json:"linkState,omitempty"`
}

// AllIPs returns the de-duplicated IPs reported for the interface.
func (s InterfaceStatus) AllIPs() []string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range append([]string{s.IP}, s.IPs...) {
		if ip != "" && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}

// Condition is a KubeVirt status condition.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// ConditionAgentConnected is set when the guest agent is connected.
const ConditionAgentConnected = "AgentConnected"

// MigrationState is the subset of migration state read by netdoctor.
type MigrationState struct {
	Completed  bool   `json:"completed,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	SourceNode string `json:"sourceNode,omitempty"`
	TargetNode string `json:"targetNode,omitempty"`
	TargetPod  string `json:"targetPod,omitempty"`
}

// InProgress reports whether a live migration is underway.
func (m *MigrationState) InProgress() bool {
	return m != nil && !m.Completed && !m.Failed
}

// FindCondition returns the condition of the given type, if present.
func FindCondition(conds []Condition, t string) (Condition, bool) {
	for _, c := range conds {
		if c.Type == t {
			return c, true
		}
	}
	return Condition{}, false
}
