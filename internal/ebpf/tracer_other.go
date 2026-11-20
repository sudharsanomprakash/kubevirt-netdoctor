//go:build !linux

package ebpf

import "context"

// Meta is caller-supplied context recorded in the trace.
type Meta struct {
	NodeName string
	Target   *Target
}

// Run is unsupported outside Linux.
func Run(context.Context, Options, Meta) (*Trace, error) { return nil, ErrUnsupported }

// NetnsID is unsupported outside Linux.
func NetnsID(string) (uint32, error) { return 0, ErrUnsupported }

// ResolveInterface is unsupported outside Linux.
func ResolveInterface(string, string) (InterfaceFilter, error) {
	return InterfaceFilter{}, ErrUnsupported
}

// PodNetns is the network namespace of a pod found on this node.
type PodNetns struct {
	Path string
	ID   uint32
	PIDs int
}

// FindPodNetns is unsupported outside Linux.
func FindPodNetns(string, string) (*PodNetns, error) { return nil, ErrUnsupported }

// ListInterfaces is unsupported outside Linux.
func ListInterfaces(string) ([]InterfaceFilter, error) { return nil, ErrUnsupported }

// ProbeCapabilities reports that eBPF is unsupported outside Linux.
func ProbeCapabilities(bool) NodeCapabilities {
	return NodeCapabilities{Tracing: CapUnavailable, Checks: []Capability{{Name: "operating system", Status: CapUnavailable, Detail: ErrUnsupported.Error()}}}
}
