//go:build linux

package ebpf

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// PodNetns is the network namespace of a pod found on this node.
type PodNetns struct {
	Path string // /proc/<pid>/ns/net of one process in the pod
	ID   uint32
	PIDs int // processes of the pod sharing it
}

// FindPodNetns finds the network namespace of the pod with the given UID by
// scanning process cgroups under procRoot (normally "/proc"). Processes in
// the host network namespace are ignored, so host-network pods are reported
// as not found. It fails if the pod's processes are in more than one
// non-host network namespace.
func FindPodNetns(procRoot, podUID string) (*PodNetns, error) {
	// The host namespace is PID 1's; if that is unreadable (hardened
	// init), fall back to our own, which is the host's when netdoctor runs
	// with host networking as documented.
	hostID, err := NetnsID(filepath.Join(procRoot, "1", "ns", "net"))
	if err != nil {
		if hostID, err = NetnsID(filepath.Join(procRoot, "self", "ns", "net")); err != nil {
			return nil, fmt.Errorf("reading the host network namespace (run in the host PID namespace as root): %w", err)
		}
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	found := map[uint32]*PodNetns{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		cg, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cgroup")) // #nosec G304 -- fixed /proc layout
		if err != nil || !cgroupHasPod(string(cg), podUID) {
			continue
		}
		path := filepath.Join(procRoot, e.Name(), "ns", "net")
		id, err := NetnsID(path)
		if err != nil || id == hostID {
			continue
		}
		if f, ok := found[id]; ok {
			f.PIDs++
			continue
		}
		found[id] = &PodNetns{Path: path, ID: id, PIDs: 1}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no process of pod %s was found in a non-host network namespace on this node (is the pod on this node, and is netdoctor in the host PID namespace?)", podUID)
	case 1:
		for _, f := range found {
			return f, nil
		}
	}
	ids := make([]uint32, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return nil, fmt.Errorf("pod %s has processes in %d network namespaces %v; refusing to guess", podUID, len(found), ids)
}

// ListInterfaces returns the non-loopback interfaces in a namespace.
func ListInterfaces(nsPath string) ([]InterfaceFilter, error) {
	id, err := NetnsID(nsPath)
	if err != nil {
		return nil, err
	}
	var ifs []net.Interface
	if err := inNetns(nsPath, func() error {
		var e error
		ifs, e = net.Interfaces()
		return e
	}); err != nil {
		return nil, err
	}
	var out []InterfaceFilter
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, InterfaceFilter{Name: i.Name, Index: uint32(i.Index), NetnsID: id}) //nolint:gosec // ifindex is a positive int
	}
	return out, nil
}
