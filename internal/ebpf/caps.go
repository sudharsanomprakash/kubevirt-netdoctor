package ebpf

// Status values in a capability report.
const (
	CapAvailable   = "Available"
	CapUnavailable = "Unavailable"
	CapDegraded    = "Degraded"
	CapNotChecked  = "NotChecked"
)

// NodeCapabilities describes what kernel observability this node supports.
type NodeCapabilities struct {
	Kernel string `json:"kernel"`
	Arch   string `json:"arch"`
	// Tracing is Available when every program loaded, Degraded when some
	// did, Unavailable otherwise.
	Tracing string       `json:"tracing"`
	Checks  []Capability `json:"checks"`
}

// Capability is one checked requirement.
type Capability struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Impact string `json:"impact,omitempty"`
}
