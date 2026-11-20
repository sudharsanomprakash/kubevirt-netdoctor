package ebpf

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// SchemaVersion versions the trace file format.
const SchemaVersion = "netdoctor.trace/v1alpha1"

// ErrUnsupported is returned on platforms without eBPF support.
var ErrUnsupported = errors.New("eBPF tracing requires Linux")

// Event types in the trace file.
const (
	TypeDrop           = "drop"
	TypeTCPRetransmit  = "tcp_retransmit"
	TypeTCPResetRecv   = "tcp_reset_received"
	TypeTCPResetSent   = "tcp_reset_sent"
	TypeTCPStateChange = "tcp_state"
)

// Event groups selectable with Options.Events.
const (
	GroupDrops      = "drops"
	GroupTCP        = "tcp"
	GroupInterfaces = "interfaces"
)

// AllGroups lists every event group, in display order.
var AllGroups = []string{GroupDrops, GroupTCP, GroupInterfaces}

// Limits enforced on every trace.
const (
	DefaultDuration  = 30 * time.Second
	MaxDuration      = 10 * time.Minute
	DefaultMaxEvents = 10000
	DefaultRingBytes = 1 << 20
	MinRingBytes     = 1 << 12
	MaxRingBytes     = 1 << 26
	MaxFilterEntries = 256
)

// Options configure a trace.
type Options struct {
	Duration time.Duration
	// Events selects event groups (GroupDrops, GroupTCP, GroupInterfaces).
	Events []string
	// Interfaces and Addresses form the in-kernel filter. An event is kept
	// when it matches either.
	Interfaces []InterfaceFilter
	Addresses  []netip.Addr
	// Unfiltered traces node-wide; events are then unattributed. It must be
	// requested explicitly and is never the default.
	Unfiltered bool
	MaxEvents  int
	RingBytes  int

	// ready, when set, is called once all programs are attached (tests).
	ready func()
}

// InterfaceFilter selects an interface by index in a network namespace.
type InterfaceFilter struct {
	Name    string `json:"name,omitempty"`
	Index   uint32 `json:"index"`
	NetnsID uint32 `json:"netns"`
}

// Validate checks options and applies defaults.
func (o *Options) Validate() error {
	if o.Duration == 0 {
		o.Duration = DefaultDuration
	}
	if o.Duration < 0 || o.Duration > MaxDuration {
		return fmt.Errorf("duration %s is outside (0, %s]", o.Duration, MaxDuration)
	}
	if len(o.Events) == 0 {
		o.Events = append([]string(nil), AllGroups...)
	}
	for _, g := range o.Events {
		if g != GroupDrops && g != GroupTCP && g != GroupInterfaces {
			return fmt.Errorf("unknown event group %q (use %s, %s or %s)", g, GroupDrops, GroupTCP, GroupInterfaces)
		}
	}
	if !o.Unfiltered && len(o.Interfaces) == 0 && len(o.Addresses) == 0 {
		return errors.New("a filter is required: interfaces and/or addresses (node-wide tracing needs the explicit unfiltered option)")
	}
	if len(o.Interfaces) > MaxFilterEntries || len(o.Addresses) > MaxFilterEntries {
		return fmt.Errorf("at most %d interfaces and %d addresses can be filtered", MaxFilterEntries, MaxFilterEntries)
	}
	if o.MaxEvents == 0 {
		o.MaxEvents = DefaultMaxEvents
	}
	if o.MaxEvents < 0 {
		return errors.New("max events must be positive")
	}
	if o.RingBytes == 0 {
		o.RingBytes = DefaultRingBytes
	}
	if o.RingBytes < MinRingBytes || o.RingBytes > MaxRingBytes || o.RingBytes&(o.RingBytes-1) != 0 {
		return fmt.Errorf("ring buffer size must be a power of two between %d and %d bytes", MinRingBytes, MaxRingBytes)
	}
	return nil
}

func (o *Options) has(group string) bool {
	for _, g := range o.Events {
		if g == group {
			return true
		}
	}
	return false
}

// Trace is the result of one trace window.
type Trace struct {
	SchemaVersion string `json:"schemaVersion"`
	NodeName      string `json:"nodeName,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Kernel        string `json:"kernel,omitempty"`
	StartedAt     string `json:"startedAt"`
	EndedAt       string `json:"endedAt"`
	// Target identifies what the filter was built for, when known (for
	// example the VM from a trace plan).
	Target *Target `json:"target,omitempty"`
	// LauncherNetns is the network namespace ID of the target's pod.
	LauncherNetns uint32            `json:"launcherNetns,omitempty"`
	Filter        Filter            `json:"filter"`
	Programs      []ProgramStatus   `json:"programs"`
	Counters      Counters          `json:"counters"`
	Overhead      Overhead          `json:"overhead"`
	Ifaces        []InterfaceCounts `json:"interfaces"`
	Events        []Event           `json:"events"`
	// Limitations explain what this trace could not observe.
	Limitations []string `json:"limitations"`
}

// Target identifies the workload a trace was filtered for.
type Target struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	PodUID    string `json:"podUID,omitempty"`
}

// Filter records the filter actually applied.
type Filter struct {
	Events     []string          `json:"events"`
	Interfaces []InterfaceFilter `json:"interfaces,omitempty"`
	Addresses  []string          `json:"addresses,omitempty"`
	Unfiltered bool              `json:"unfiltered,omitempty"`
	MaxEvents  int               `json:"maxEvents"`
	RingBytes  int               `json:"ringBytes"`
}

// ProgramStatus reports one kernel program.
type ProgramStatus struct {
	Name       string `json:"name"`
	Hook       string `json:"hook"`
	Attached   bool   `json:"attached"`
	Error      string `json:"error,omitempty"`
	RunCount   uint64 `json:"runCount"`
	RunTimeNs  uint64 `json:"runTimeNs"`
	StatsValid bool   `json:"statsValid"`
}

// Counters are kernel-side totals of matched events. They are counted
// even when an event could not be delivered.
type Counters struct {
	Drops           uint64 `json:"drops"`
	TCPRetransmits  uint64 `json:"tcpRetransmits"`
	TCPResetsRecv   uint64 `json:"tcpResetsReceived"`
	TCPResetsSent   uint64 `json:"tcpResetsSent"`
	TCPStateChanges uint64 `json:"tcpStateChanges"`
	// LostEvents were matched but the ring buffer was full.
	LostEvents uint64 `json:"lostEvents"`
	// SuppressedEvents were delivered after MaxEvents and not recorded.
	SuppressedEvents uint64 `json:"suppressedEvents"`
}

// Overhead is the measured cost of the instrumentation.
type Overhead struct {
	// Measured is false when the kernel could not report BPF run-time
	// statistics (requires Linux 5.8 and CAP_SYS_ADMIN).
	Measured      bool    `json:"measured"`
	TotalRunNs    uint64  `json:"totalRunTimeNs"`
	TotalRuns     uint64  `json:"totalRuns"`
	AvgNsPerRun   float64 `json:"avgNsPerRun"`
	CPUPercentOne float64 `json:"cpuPercentOfOneCore"`
}

// InterfaceCounts are packets matching the filter, per interface and
// direction, counted at netif_receive_skb (rx) and net_dev_queue (tx).
type InterfaceCounts struct {
	Name      string `json:"name,omitempty"`
	Index     uint32 `json:"index"`
	NetnsID   uint32 `json:"netns"`
	Direction string `json:"direction"`
	Packets   uint64 `json:"packets"`
	Bytes     uint64 `json:"bytes"`
}

// Event is one normalized kernel observation.
type Event struct {
	Time       string   `json:"time"`
	Type       string   `json:"type"`
	Interface  string   `json:"interface,omitempty"`
	IfIndex    uint32   `json:"ifindex,omitempty"`
	NetnsID    uint32   `json:"netns,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Src        string   `json:"src,omitempty"`
	Dst        string   `json:"dst,omitempty"`
	SrcPort    uint16   `json:"srcPort,omitempty"`
	DstPort    uint16   `json:"dstPort,omitempty"`
	Length     uint32   `json:"length,omitempty"`
	DropReason string   `json:"dropReason,omitempty"`
	Location   string   `json:"location,omitempty"`
	OldState   string   `json:"oldState,omitempty"`
	NewState   string   `json:"newState,omitempty"`
	SRTTMicros uint32   `json:"srttMicros,omitempty"`
	PID        uint32   `json:"pid,omitempty"`
	MatchedBy  []string `json:"matchedBy"`
	// Attribution says how strongly the event is tied to the target
	// workload (see package traceplan). Empty when no target was given.
	Attribution string `json:"attribution,omitempty"`
}

// tcpStates are the kernel's TCP state names (include/net/tcp_states.h).
var tcpStates = map[uint8]string{
	1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1", 5: "FIN_WAIT2",
	6: "TIME_WAIT", 7: "CLOSE", 8: "CLOSE_WAIT", 9: "LAST_ACK", 10: "LISTEN",
	11: "CLOSING", 12: "NEW_SYN_RECV", 13: "BOUND_INACTIVE",
}

// TCPStateName returns the kernel name of a TCP state.
func TCPStateName(s uint8) string {
	if n, ok := tcpStates[s]; ok {
		return n
	}
	return fmt.Sprintf("STATE_%d", s)
}

var protocols = map[uint8]string{1: "icmp", 6: "tcp", 17: "udp", 58: "icmpv6"}

// ProtocolName returns a protocol name, or its number.
func ProtocolName(p uint8) string {
	if p == 0 {
		return ""
	}
	if n, ok := protocols[p]; ok {
		return n
	}
	return fmt.Sprintf("ip-proto-%d", p)
}

// addrFrom converts a 16-byte kernel address (IPv4-mapped for IPv4).
func addrFrom(b [16]uint8, family uint16) string {
	if family != 2 && family != 10 {
		return ""
	}
	a := netip.AddrFrom16(b).Unmap()
	if !a.IsValid() || a.IsUnspecified() {
		return ""
	}
	return a.String()
}

// AddrKey converts an address to the 16-byte filter key.
func AddrKey(a netip.Addr) [16]uint8 {
	if a.Is4() {
		return netip.AddrFrom16(a.As16()).As16()
	}
	return a.As16()
}
