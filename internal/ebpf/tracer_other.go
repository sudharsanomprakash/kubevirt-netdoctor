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
