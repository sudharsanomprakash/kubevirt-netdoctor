// Package topology holds the observed network topology of a KubeVirt VM and
// the logic that correlates its layers: VMI network spec, guest interface
// status, virt-launcher pod attachments and NetworkAttachmentDefinitions.
//
// Everything in this model is what the Kubernetes API reported. Nothing here
// is inferred from, or verified by, data-plane traffic.
package topology

import (
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
)

// Target is the object the user asked to inspect.
type Target struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Target kinds.
const (
	KindVM  = "VirtualMachine"
	KindVMI = "VirtualMachineInstance"
)

// Topology is the full observed state for one VM/VMI.
type Topology struct {
	Target         Target        `json:"target"`
	VirtualMachine *VMInfo       `json:"virtualMachine,omitempty"`
	Instance       *VMIInfo      `json:"virtualMachineInstance"`
	Launcher       *LauncherInfo `json:"launcher"`
	// NodeName is the node hosting the VMI, as reported by VMI status.
	NodeName string        `json:"nodeName,omitempty"`
	Networks []NetworkPath `json:"networks"`
	// UnmatchedGuestInterfaces are named VMI status interfaces that have no
	// corresponding spec network.
	UnmatchedGuestInterfaces []GuestInterface `json:"unmatchedGuestInterfaces,omitempty"`
	// GuestOnlyInterfaces are interfaces the guest agent reports that are
	// not backed by a VMI network (for example a container bridge inside the
	// guest). They are informational.
	GuestOnlyInterfaces []GuestInterface   `json:"guestOnlyInterfaces,omitempty"`
	AccessIssues        []kube.AccessIssue `json:"accessIssues,omitempty"`
}

// VMInfo summarizes a VirtualMachine.
type VMInfo struct {
	Name            string           `json:"name"`
	Lookup          kube.LookupState `json:"lookup"`
	RunStrategy     string           `json:"runStrategy,omitempty"`
	PrintableStatus string           `json:"printableStatus,omitempty"`
	Ready           bool             `json:"ready"`
}

// Guest agent states.
const (
	AgentConnected    = "Connected"
	AgentNotConnected = "NotConnected"
	AgentUnknown      = "Unknown"
)

// Network spec sources.
const (
	SpecFromVMI        = "VirtualMachineInstance"
	SpecFromVMTemplate = "VirtualMachineTemplate"
)

// VMIInfo summarizes a VirtualMachineInstance.
type VMIInfo struct {
	Name     string           `json:"name"`
	Lookup   kube.LookupState `json:"lookup"`
	UID      string           `json:"uid,omitempty"`
	Phase    string           `json:"phase,omitempty"`
	Reason   string           `json:"reason,omitempty"`
	NodeName string           `json:"nodeName,omitempty"`
	// GuestAgent is derived from the AgentConnected condition.
	GuestAgent          string               `json:"guestAgent,omitempty"`
	MigrationInProgress bool                 `json:"migrationInProgress,omitempty"`
	Conditions          []kubevirt.Condition `json:"conditions,omitempty"`
	// NetworkSpecSource says where the network spec was read from: the VMI,
	// or the VM template when no VMI exists.
	NetworkSpecSource string `json:"networkSpecSource"`
}

// LauncherInfo describes virt-launcher pod discovery.
type LauncherInfo struct {
	Lookup          kube.LookupState `json:"lookup"`
	Selected        *PodInfo         `json:"selected,omitempty"`
	Candidates      []PodInfo        `json:"candidates,omitempty"`
	Terminated      []PodInfo        `json:"terminated,omitempty"`
	SelectionReason string           `json:"selectionReason,omitempty"`
	// Multus annotation state on the selected pod.
	NetworkStatusPresent bool   `json:"networkStatusPresent"`
	NetworkStatusError   string `json:"networkStatusError,omitempty"`
	NetworksError        string `json:"networksAnnotationError,omitempty"`
	DefaultNetwork       string `json:"multusDefaultNetwork,omitempty"`
}

// PodInfo summarizes a pod.
type PodInfo struct {
	Name     string   `json:"name"`
	UID      string   `json:"uid"`
	Phase    string   `json:"phase"`
	NodeName string   `json:"nodeName,omitempty"`
	PodIPs   []string `json:"podIPs,omitempty"`
}

// Network source types.
const (
	SourcePod     = "pod"
	SourceMultus  = "multus"
	SourceUnknown = "unknown"
)

// NetworkPath is one VMI network correlated across all observable layers:
//
//	spec interface -> guest interface (VMI status) -> launcher pod
//	attachment (Multus) -> NetworkAttachmentDefinition (CNI config)
//
// A nil layer means it was not observed.
type NetworkPath struct {
	Name          string          `json:"name"`
	Source        string          `json:"source"`
	MultusDefault bool            `json:"multusDefault,omitempty"`
	Interface     *SpecInterface  `json:"interface,omitempty"`
	Guest         *GuestInterface `json:"guest,omitempty"`
	PodRequest    *PodRequest     `json:"podRequest,omitempty"`
	PodAttachment *PodAttachment  `json:"podAttachment,omitempty"`
	NAD           *NADInfo        `json:"networkAttachmentDefinition,omitempty"`
}

// SpecInterface is the interface definition from the VMI spec.
type SpecInterface struct {
	Binding string `json:"binding"`
	MAC     string `json:"macAddress,omitempty"`
	Model   string `json:"model,omitempty"`
}

// GuestInterface is an interface as reported in VMI status.
type GuestInterface struct {
	Name             string   `json:"name,omitempty"`
	InterfaceName    string   `json:"interfaceName,omitempty"`
	MAC              string   `json:"mac,omitempty"`
	IPs              []string `json:"ips,omitempty"`
	InfoSource       string   `json:"infoSource,omitempty"`
	PodInterfaceName string   `json:"podInterfaceName,omitempty"`
	LinkState        string   `json:"linkState,omitempty"`
}

// PodRequest is the attachment KubeVirt requested on the launcher pod via
// the Multus networks annotation.
type PodRequest struct {
	Interface string `json:"interface,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

// PodAttachment is the attachment Multus reported in network-status.
type PodAttachment struct {
	NetworkName string   `json:"networkName"`
	Interface   string   `json:"interface,omitempty"`
	MAC         string   `json:"mac,omitempty"`
	IPs         []string `json:"ips,omitempty"`
	Default     bool     `json:"default,omitempty"`
}

// NADInfo is the NetworkAttachmentDefinition referenced by a Multus network.
type NADInfo struct {
	Ref          multus.NetworkRef `json:"ref"`
	RefError     string            `json:"refError,omitempty"`
	Lookup       kube.LookupState  `json:"lookup"`
	Message      string            `json:"message,omitempty"`
	ResourceName string            `json:"resourceName,omitempty"`
	Config       *multus.CNIConfig `json:"config,omitempty"`
	// ConfigError is set when spec.config could not be parsed.
	ConfigError string `json:"configError,omitempty"`
	// ConfigNotInline is set when spec.config is empty and the CNI
	// configuration lives in a node-local file that cannot be observed.
	ConfigNotInline bool `json:"configNotInline,omitempty"`
}
