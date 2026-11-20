// Package traceplan connects kernel traces to KubeVirt VMs.
//
// A Plan is built from the Kubernetes topology (no privileges needed) and
// tells the node-side tracer which virt-launcher pod and addresses belong to
// the VM. Attribute then classifies every traced event by how strongly it
// can be tied to the VM.
package traceplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// SchemaVersion versions the plan file format.
const SchemaVersion = "netdoctor.traceplan/v1alpha1"

// Attribution levels, from strongest to weakest.
const (
	// DirectlyAttributed: observed on an interface inside the VM's
	// virt-launcher network namespace.
	DirectlyAttributed = "DirectlyAttributed"
	// Correlated: matched by an address that belongs only to this VM.
	Correlated = "Correlated"
	// Ambiguous: matched by an address that other workloads can share
	// (masquerade guest addresses, link-local addresses).
	Ambiguous = "Ambiguous"
	// Unattributed: not tied to the VM (node-wide trace).
	Unattributed = "Unattributed"
)

// Plan describes what to trace for one VM.
type Plan struct {
	SchemaVersion string      `json:"schemaVersion"`
	CreatedAt     string      `json:"createdAt"`
	Target        ebpf.Target `json:"target"`
	// VMIName is the instance name; Target.Name may be the VM.
	VMIName  string `json:"vmiName"`
	NodeName string `json:"nodeName"`
	// LauncherPod is the virt-launcher pod whose network namespace the
	// node tracer filters on.
	LauncherPod string    `json:"launcherPod"`
	PodUID      string    `json:"podUID"`
	Addresses   []Address `json:"addresses"`
	Events      []string  `json:"events"`
	Duration    string    `json:"duration"`
	// Notes explain gaps in the plan.
	Notes []string `json:"notes"`
}

// Address is a VM address used as a filter.
type Address struct {
	IP      string `json:"ip"`
	Network string `json:"network"`
	// Source is where the address was reported: guest (VMI status) or pod
	// (Multus network-status / pod IPs).
	Source string `json:"source"`
	// Ambiguous addresses can belong to other workloads too.
	Ambiguous bool   `json:"ambiguous,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Build creates a plan from a discovered topology.
func Build(t *topology.Topology, events []string, duration time.Duration, now time.Time) (*Plan, error) {
	if t.Instance == nil || t.Instance.Name == "" || t.Instance.UID == "" {
		return nil, errors.New("the VM has no running VirtualMachineInstance to trace")
	}
	if t.Launcher == nil || t.Launcher.Selected == nil || t.Launcher.Selected.UID == "" {
		return nil, errors.New("no single virt-launcher pod was identified for the VMI; run 'inspect' to see why")
	}
	node := t.NodeName
	if node == "" {
		node = t.Launcher.Selected.NodeName
	}
	if node == "" {
		return nil, errors.New("the VMI is not scheduled to a node")
	}
	p := &Plan{
		SchemaVersion: SchemaVersion,
		CreatedAt:     now.UTC().Format(time.RFC3339),
		Target: ebpf.Target{
			Kind:      t.Target.Kind,
			Namespace: t.Target.Namespace,
			Name:      t.Target.Name,
			PodUID:    t.Launcher.Selected.UID,
		},
		VMIName:     t.Instance.Name,
		NodeName:    node,
		LauncherPod: t.Launcher.Selected.Name,
		PodUID:      t.Launcher.Selected.UID,
		Addresses:   []Address{},
		Events:      events,
		Duration:    duration.String(),
		Notes:       []string{},
	}
	seen := map[string]bool{}
	add := func(ip, network, source string) {
		a, err := netip.ParseAddr(strings.TrimSpace(ip))
		if err != nil || seen[a.String()] {
			return
		}
		seen[a.String()] = true
		addr := Address{IP: a.String(), Network: network, Source: source}
		switch {
		case a.IsLinkLocalUnicast():
			addr.Ambiguous, addr.Reason = true, "link-local addresses are reused on every link"
		case a.IsLoopback() || a.IsUnspecified() || a.IsMulticast():
			return
		case masqueradeInternal(a):
			addr.Ambiguous, addr.Reason = true, "inside KubeVirt's masquerade range, which every masquerade VM reuses"
		}
		p.Addresses = append(p.Addresses, addr)
	}
	// Pod-side addresses first: they are allocated by IPAM and unique.
	for _, n := range t.Networks {
		if n.PodAttachment != nil {
			for _, ip := range n.PodAttachment.IPs {
				add(ip, n.Name, "pod")
			}
		}
	}
	for _, ip := range t.Launcher.Selected.PodIPs {
		add(ip, "pod", "pod")
	}
	for _, n := range t.Networks {
		if n.Guest != nil {
			for _, ip := range n.Guest.IPs {
				add(ip, n.Name, "guest")
			}
		}
	}
	sort.SliceStable(p.Addresses, func(i, j int) bool { return p.Addresses[i].IP < p.Addresses[j].IP })
	if len(p.Addresses) == 0 {
		p.Notes = append(p.Notes, "no VM addresses are reported (no guest agent and no IPAM); only events on the launcher pod's interfaces can be attributed")
	}
	if t.Instance.MigrationInProgress {
		p.Notes = append(p.Notes, "a live migration is in progress; the VM may move to another node during the trace")
	}
	return p, nil
}

// KubeVirt's default masquerade networks (vmNetworkCIDR / vmIPv6NetworkCIDR).
var masqueradeRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.2.0/24"),
	netip.MustParsePrefix("fd10:0:2::/120"),
}

func masqueradeInternal(a netip.Addr) bool {
	for _, p := range masqueradeRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Filters returns the address filter for the node tracer.
func (p *Plan) Filters() []netip.Addr {
	var out []netip.Addr
	for _, a := range p.Addresses {
		if ip, err := netip.ParseAddr(a.IP); err == nil {
			out = append(out, ip)
		}
	}
	return out
}

// Validate checks a loaded plan.
func (p *Plan) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported trace plan schema %q (want %q)", p.SchemaVersion, SchemaVersion)
	}
	if p.PodUID == "" || p.NodeName == "" {
		return errors.New("trace plan is missing the pod UID or node name")
	}
	return nil
}

// Load reads a plan file.
func Load(path string) (*Plan, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- explicit CLI argument
	if err != nil {
		return nil, fmt.Errorf("reading trace plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parsing trace plan %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadTrace reads a trace file.
func LoadTrace(path string) (*ebpf.Trace, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- explicit CLI argument
	if err != nil {
		return nil, fmt.Errorf("reading trace: %w", err)
	}
	var tr ebpf.Trace
	if err := json.Unmarshal(b, &tr); err != nil {
		return nil, fmt.Errorf("parsing trace %s: %w", path, err)
	}
	if tr.SchemaVersion != ebpf.SchemaVersion {
		return nil, fmt.Errorf("unsupported trace schema %q (want %q)", tr.SchemaVersion, ebpf.SchemaVersion)
	}
	return &tr, nil
}

// Attribute sets the attribution of every event. launcherNetns is the
// network namespace ID of the VM's virt-launcher pod (0 if unknown);
// addresses are the plan's addresses.
func Attribute(tr *ebpf.Trace, launcherNetns uint32, addresses []Address) {
	byIP := map[string]Address{}
	for _, a := range addresses {
		byIP[a.IP] = a
	}
	for i := range tr.Events {
		tr.Events[i].Attribution = attribution(&tr.Events[i], launcherNetns, byIP)
	}
}

func attribution(e *ebpf.Event, launcherNetns uint32, byIP map[string]Address) string {
	iface, addr := false, false
	for _, m := range e.MatchedBy {
		switch m {
		case "interface":
			iface = true
		case "address":
			addr = true
		}
	}
	if iface && launcherNetns != 0 && e.NetnsID == launcherNetns {
		return DirectlyAttributed
	}
	if addr {
		amb, found := false, false
		for _, ip := range []string{e.Src, e.Dst} {
			if a, ok := byIP[ip]; ok {
				found = true
				if a.Ambiguous {
					amb = true
				} else {
					return Correlated
				}
			}
		}
		if found && amb {
			return Ambiguous
		}
	}
	return Unattributed
}

// Summary aggregates attributed events.
type Summary struct {
	ByAttribution map[string]int `json:"byAttribution"`
	// DropReasons counts drops per reason, for directly attributed and
	// correlated events.
	DropReasons   map[string]int `json:"dropReasons"`
	Retransmits   int            `json:"retransmits"`
	ResetsSent    int            `json:"resetsSent"`
	ResetsRecv    int            `json:"resetsReceived"`
	AmbiguousOnly int            `json:"ambiguousOnly"`
}

// Summarize counts events that are tied to the VM (directly attributed or
// correlated). Ambiguous events are counted separately.
func Summarize(tr *ebpf.Trace) Summary {
	s := Summary{ByAttribution: map[string]int{}, DropReasons: map[string]int{}}
	for _, e := range tr.Events {
		s.ByAttribution[e.Attribution]++
		if e.Attribution == Ambiguous {
			s.AmbiguousOnly++
			continue
		}
		if e.Attribution != DirectlyAttributed && e.Attribution != Correlated {
			continue
		}
		switch e.Type {
		case ebpf.TypeDrop:
			s.DropReasons[e.DropReason]++
		case ebpf.TypeTCPRetransmit:
			s.Retransmits++
		case ebpf.TypeTCPResetSent:
			s.ResetsSent++
		case ebpf.TypeTCPResetRecv:
			s.ResetsRecv++
		}
	}
	return s
}
