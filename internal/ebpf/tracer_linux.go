//go:build linux

package ebpf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

// Kernel-side constants mirrored from bpf/netdoctor.bpf.c.
const (
	evDrop          = 1
	evTCPRetransmit = 2
	evTCPResetRx    = 3
	evTCPResetTx    = 4
	evTCPState      = 5

	maskDrops = 1 << 0
	maskTCP   = 1 << 1
	maskIface = 1 << 2

	cDrop          = 0
	cTCPRetransmit = 1
	cTCPResetRx    = 2
	cTCPResetTx    = 3
	cTCPState      = 4
	cRingbufLost   = 5
	cMax           = 6
)

// programGroups maps each kernel program to the event group that needs it.
var programGroups = map[string]string{
	"on_kfree_skb":           GroupDrops,
	"on_tcp_retransmit":      GroupTCP,
	"on_tcp_receive_reset":   GroupTCP,
	"on_tcp_send_reset":      GroupTCP,
	"on_inet_sock_set_state": GroupTCP,
	"on_netif_receive_skb":   GroupInterfaces,
	"on_net_dev_queue":       GroupInterfaces,
}

// Meta is caller-supplied context recorded in the trace.
type Meta struct {
	NodeName string
	Target   *Target
}

// Run loads the tracer, records for opts.Duration (or until ctx is
// canceled), detaches and returns the trace. It needs CAP_BPF and
// CAP_PERFMON (or root) and a kernel with BTF.
func Run(ctx context.Context, opts Options, meta Meta) (*Trace, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	kspec, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("kernel BTF unavailable (need CONFIG_DEBUG_INFO_BTF): %w", err)
	}
	// Kernels since 5.11 account BPF memory to the cgroup and ignore
	// RLIMIT_MEMLOCK; on older ones a failure here surfaces at load time.
	_ = rlimit.RemoveMemlock()
	spec, err := loadNetdoctor()
	if err != nil {
		return nil, fmt.Errorf("loading embedded BPF objects: %w", err)
	}

	tr := &Trace{
		SchemaVersion: SchemaVersion,
		NodeName:      meta.NodeName,
		Target:        meta.Target,
		Filter: Filter{
			Events:     opts.Events,
			Interfaces: opts.Interfaces,
			Unfiltered: opts.Unfiltered,
			MaxEvents:  opts.MaxEvents,
			RingBytes:  opts.RingBytes,
		},
		Programs:    []ProgramStatus{},
		Ifaces:      []InterfaceCounts{},
		Events:      []Event{},
		Limitations: []string{},
	}
	for _, a := range opts.Addresses {
		tr.Filter.Addresses = append(tr.Filter.Addresses, a.String())
	}
	if h, err := os.Hostname(); err == nil {
		tr.Hostname = h
	}
	var uts unix.Utsname
	if unix.Uname(&uts) == nil {
		tr.Kernel = unix.ByteSliceToString(uts.Release[:])
	}

	// Drop programs that are not needed or whose tracepoint this kernel
	// lacks, so one missing hook degrades the trace instead of failing it.
	names := make([]string, 0, len(programGroups))
	for n := range programGroups {
		names = append(names, n)
	}
	sort.Strings(names)
	status := map[string]*ProgramStatus{}
	var mask uint32
	for _, n := range names {
		ps, ok := spec.Programs[n]
		if !ok {
			continue
		}
		st := &ProgramStatus{Name: n, Hook: ps.AttachTo}
		status[n] = st
		group := programGroups[n]
		if !opts.has(group) {
			delete(spec.Programs, n)
			st.Error = "not requested"
			continue
		}
		var tp *btf.Typedef
		if err := kspec.TypeByName("btf_trace_"+ps.AttachTo, &tp); err != nil {
			delete(spec.Programs, n)
			st.Error = "tracepoint not available on this kernel"
			tr.Limitations = append(tr.Limitations, fmt.Sprintf("tracepoint %s is not available on this kernel; %s events from it are not observed", ps.AttachTo, group))
			continue
		}
		switch group {
		case GroupDrops:
			mask |= maskDrops
		case GroupTCP:
			mask |= maskTCP
		case GroupInterfaces:
			mask |= maskIface
		}
	}

	spec.Maps["events"].MaxEntries = uint32(opts.RingBytes) //nolint:gosec // bounded by Validate to MaxRingBytes
	set := func(name string, v uint32) error {
		vs, ok := spec.Variables[name]
		if !ok {
			return fmt.Errorf("BPF object has no variable %s", name)
		}
		return vs.Set(v)
	}
	b2u := func(b bool) uint32 {
		if b {
			return 1
		}
		return 0
	}
	notDropped, consumed := nonDropReasons(kspec)
	for name, v := range map[string]uint32{
		"cfg_reason_not_dropped": notDropped,
		"cfg_reason_consumed":    consumed,
		"cfg_event_mask":         mask,
		"cfg_filter_ifindex":     b2u(len(opts.Interfaces) > 0),
		"cfg_filter_addr":        b2u(len(opts.Addresses) > 0),
		"cfg_unfiltered":         b2u(opts.Unfiltered),
	} {
		if err := set(name, v); err != nil {
			return nil, err
		}
	}

	coll, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
		Programs: ebpf.ProgramOptions{KernelTypes: kspec},
	})
	if err != nil {
		if errors.Is(err, unix.EPERM) {
			return nil, fmt.Errorf("loading BPF programs was not permitted (run as root or grant CAP_BPF, CAP_PERFMON): %w", err)
		}
		return nil, fmt.Errorf("loading BPF programs: %w", err)
	}
	defer coll.Close()

	for _, f := range opts.Interfaces {
		k := netdoctorIfkey{Netns: f.NetnsID, Ifindex: f.Index}
		if err := coll.Maps["filter_ifaces"].Put(k, uint8(1)); err != nil {
			return nil, fmt.Errorf("populating the interface filter: %w", err)
		}
	}
	for _, a := range opts.Addresses {
		k := netdoctorAddrkey{Addr: AddrKey(a)}
		if err := coll.Maps["filter_addrs"].Put(k, uint8(1)); err != nil {
			return nil, fmt.Errorf("populating the address filter: %w", err)
		}
	}

	// Run-time statistics measure the tracer's own overhead.
	var statsCloser interface{ Close() error }
	if c, err := ebpf.EnableStats(uint32(unix.BPF_STATS_RUN_TIME)); err == nil {
		statsCloser = c
	} else {
		tr.Limitations = append(tr.Limitations, "BPF run-time statistics are unavailable, so overhead was not measured: "+err.Error())
	}

	rd, err := ringbuf.NewReader(coll.Maps["events"])
	if err != nil {
		return nil, fmt.Errorf("opening the ring buffer: %w", err)
	}
	defer rd.Close()

	var links []link.Link
	detach := func() {
		for _, l := range links {
			_ = l.Close()
		}
		links = nil
	}
	defer detach()
	for _, n := range names {
		prog, ok := coll.Programs[n]
		if !ok {
			continue
		}
		l, err := link.AttachTracing(link.TracingOptions{Program: prog})
		if err != nil {
			status[n].Error = err.Error()
			tr.Limitations = append(tr.Limitations, fmt.Sprintf("attaching %s failed: %v", status[n].Hook, err))
			continue
		}
		status[n].Attached = true
		links = append(links, l)
	}
	if len(links) == 0 {
		return nil, errors.New("no BPF program could be attached")
	}

	clk := newClock()
	if opts.ready != nil {
		opts.ready()
	}
	start := time.Now()
	tr.StartedAt = start.UTC().Format(time.RFC3339Nano)
	deadline := start.Add(opts.Duration)

	conv := newConverter(kspec, opts.Interfaces)
	var suppressed uint64
	handle := func(rec ringbuf.Record) {
		var raw netdoctorEvent
		if err := decodeEvent(rec.RawSample, &raw); err != nil {
			return
		}
		if len(tr.Events) >= opts.MaxEvents {
			suppressed++
			return
		}
		tr.Events = append(tr.Events, conv.event(&raw, clk))
	}

	// Record until the window ends or the caller cancels.
	for {
		if ctx.Err() != nil {
			tr.Limitations = append(tr.Limitations, "trace was stopped early: "+ctx.Err().Error())
			break
		}
		next := time.Now().Add(250 * time.Millisecond)
		if next.After(deadline) {
			next = deadline
		}
		rd.SetDeadline(next)
		rec, err := rd.Read()
		if err == nil {
			handle(rec)
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if !time.Now().Before(deadline) {
				break
			}
			continue
		}
		if errors.Is(err, ringbuf.ErrClosed) {
			break
		}
		return nil, fmt.Errorf("reading the ring buffer: %w", err)
	}

	// Detach first so no new events arrive, then drain what is buffered.
	end := time.Now()
	detach()
	rd.SetDeadline(time.Now())
	for {
		rec, err := rd.Read()
		if err != nil {
			break
		}
		handle(rec)
	}
	tr.EndedAt = end.UTC().Format(time.RFC3339Nano)

	// Overhead statistics are read while stats are still enabled.
	window := end.Sub(start)
	for _, n := range names {
		st := status[n]
		if prog, ok := coll.Programs[n]; ok && statsCloser != nil {
			if s, err := prog.Stats(); err == nil {
				st.RunCount = s.RunCount
				st.RunTimeNs = uint64(s.Runtime.Nanoseconds()) //nolint:gosec // run time is never negative
				st.StatsValid = true
				tr.Overhead.TotalRuns += st.RunCount
				tr.Overhead.TotalRunNs += st.RunTimeNs
			}
		}
		tr.Programs = append(tr.Programs, *st)
	}
	if statsCloser != nil {
		_ = statsCloser.Close()
		tr.Overhead.Measured = true
		if tr.Overhead.TotalRuns > 0 {
			tr.Overhead.AvgNsPerRun = float64(tr.Overhead.TotalRunNs) / float64(tr.Overhead.TotalRuns)
		}
		if window > 0 {
			tr.Overhead.CPUPercentOne = float64(tr.Overhead.TotalRunNs) / float64(window.Nanoseconds()) * 100
		}
	}

	c, err := readCounters(coll.Maps["counters"])
	if err != nil {
		return nil, err
	}
	tr.Counters = Counters{
		Drops:            c[cDrop],
		TCPRetransmits:   c[cTCPRetransmit],
		TCPResetsRecv:    c[cTCPResetRx],
		TCPResetsSent:    c[cTCPResetTx],
		TCPStateChanges:  c[cTCPState],
		LostEvents:       c[cRingbufLost],
		SuppressedEvents: suppressed,
	}
	if tr.Counters.LostEvents > 0 {
		tr.Limitations = append(tr.Limitations, fmt.Sprintf("%d events were lost because the ring buffer was full; counters are complete but the event list is not (increase the ring buffer size or narrow the filter)", tr.Counters.LostEvents))
	}
	if suppressed > 0 {
		tr.Limitations = append(tr.Limitations, fmt.Sprintf("%d events beyond the %d-event limit were not recorded; counters are complete", suppressed, opts.MaxEvents))
	}
	if opts.Unfiltered {
		tr.Limitations = append(tr.Limitations, "node-wide trace: events are not attributed to any workload")
	}

	ifs, err := readIfaceStats(coll.Maps["iface_stats"], conv)
	if err != nil {
		return nil, err
	}
	tr.Ifaces = ifs
	return tr, nil
}

func decodeEvent(b []byte, ev *netdoctorEvent) error {
	return decodeLE(b, ev)
}

func readCounters(m *ebpf.Map) ([cMax]uint64, error) {
	var out [cMax]uint64
	for i := uint32(0); i < cMax; i++ {
		var per []uint64
		if err := m.Lookup(i, &per); err != nil {
			return out, fmt.Errorf("reading counter %d: %w", i, err)
		}
		for _, v := range per {
			out[i] += v
		}
	}
	return out, nil
}

func readIfaceStats(m *ebpf.Map, conv *converter) ([]InterfaceCounts, error) {
	out := []InterfaceCounts{}
	var k netdoctorIfstatkey
	var v netdoctorIfstat
	it := m.Iterate()
	for it.Next(&k, &v) {
		dir := "rx"
		if k.Dir == 1 {
			dir = "tx"
		}
		out = append(out, InterfaceCounts{
			Name:      conv.ifname(k.Netns, k.Ifindex),
			Index:     k.Ifindex,
			NetnsID:   k.Netns,
			Direction: dir,
			Packets:   v.Packets,
			Bytes:     v.Bytes,
		})
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("reading interface counters: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.NetnsID != b.NetnsID {
			return a.NetnsID < b.NetnsID
		}
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		return a.Direction < b.Direction
	})
	return out, nil
}

// clock converts bpf_ktime_get_ns (CLOCK_MONOTONIC) to wall time.
type clock struct {
	wall time.Time
	mono int64
}

func newClock() clock {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return clock{wall: time.Now(), mono: ts.Nano()}
}

func (c clock) at(ns uint64) time.Time {
	return c.wall.Add(time.Duration(int64(ns) - c.mono)) //nolint:gosec // monotonic ns fit in int64 for 292 years
}

// converter normalizes raw events.
type converter struct {
	reasons  map[uint32]string
	names    map[netdoctorIfkey]string
	hostNS   uint32
	syms     *kallsyms
	symsDone bool
}

func newConverter(kspec *btf.Spec, ifaces []InterfaceFilter) *converter {
	c := &converter{
		reasons: dropReasons(kspec),
		names:   map[netdoctorIfkey]string{},
	}
	for _, f := range ifaces {
		if f.Name != "" {
			c.names[netdoctorIfkey{Netns: f.NetnsID, Ifindex: f.Index}] = f.Name
		}
	}
	if id, err := NetnsID("/proc/self/ns/net"); err == nil {
		c.hostNS = id
	}
	return c
}

func (c *converter) ifname(netns, idx uint32) string {
	if idx == 0 {
		return ""
	}
	k := netdoctorIfkey{Netns: netns, Ifindex: idx}
	if n, ok := c.names[k]; ok {
		return n
	}
	name := ""
	if netns == c.hostNS {
		if ifi, err := net.InterfaceByIndex(int(idx)); err == nil {
			name = ifi.Name
		}
	}
	c.names[k] = name
	return name
}

func (c *converter) location(addr uint64) string {
	if addr == 0 {
		return ""
	}
	if !c.symsDone {
		c.symsDone = true
		c.syms, _ = loadKallsyms("/proc/kallsyms")
	}
	if c.syms == nil {
		return fmt.Sprintf("0x%x", addr)
	}
	return c.syms.resolve(addr)
}

func (c *converter) event(r *netdoctorEvent, clk clock) Event {
	e := Event{
		Time:     clk.at(r.TsNs).UTC().Format(time.RFC3339Nano),
		IfIndex:  r.Ifindex,
		NetnsID:  r.Netns,
		Protocol: ProtocolName(r.Proto),
		Src:      addrFrom(r.Saddr, r.Family),
		Dst:      addrFrom(r.Daddr, r.Family),
		SrcPort:  r.Sport,
		DstPort:  r.Dport,
		Length:   r.Len,
		PID:      r.Pid,
	}
	e.Interface = c.ifname(r.Netns, r.Ifindex)
	switch r.Type {
	case evDrop:
		e.Type = TypeDrop
		e.DropReason = c.reason(r.Reason)
		e.Location = c.location(r.Location)
	case evTCPRetransmit:
		e.Type = TypeTCPRetransmit
		e.SRTTMicros = r.SrttUs
	case evTCPResetRx:
		e.Type = TypeTCPResetRecv
	case evTCPResetTx:
		e.Type = TypeTCPResetSent
	case evTCPState:
		e.Type = TypeTCPStateChange
		e.OldState = TCPStateName(r.Oldstate)
		e.NewState = TCPStateName(r.Newstate)
	default:
		e.Type = fmt.Sprintf("unknown_%d", r.Type)
	}
	e.MatchedBy = matchedBy(r.Matched)
	return e
}

func (c *converter) reason(r uint32) string {
	if n, ok := c.reasons[r]; ok {
		return n
	}
	return fmt.Sprintf("REASON_%d", r)
}

func matchedBy(bits uint8) []string {
	out := []string{}
	if bits&1 != 0 {
		out = append(out, "interface")
	}
	if bits&2 != 0 {
		out = append(out, "address")
	}
	if bits&0x80 != 0 {
		out = append(out, "unfiltered")
	}
	return out
}

// nonDropReasons returns the values of SKB_NOT_DROPPED_YET and SKB_CONSUMED
// in this kernel's enum skb_drop_reason, or ^0 for each that does not exist
// (before 6.0 every kfree_skb reason is a drop).
func nonDropReasons(kspec *btf.Spec) (notDropped, consumed uint32) {
	var en *btf.Enum
	if kspec == nil || kspec.TypeByName("skb_drop_reason", &en) != nil {
		return ^uint32(0), ^uint32(0)
	}
	return nonDropReasonsFrom(en)
}

func nonDropReasonsFrom(en *btf.Enum) (notDropped, consumed uint32) {
	notDropped, consumed = ^uint32(0), ^uint32(0)
	for _, v := range en.Values {
		switch v.Name {
		case "SKB_NOT_DROPPED_YET":
			notDropped = uint32(v.Value) //nolint:gosec // small enum
		case "SKB_CONSUMED":
			consumed = uint32(v.Value) //nolint:gosec // small enum
		}
	}
	return
}

// dropReasons reads enum skb_drop_reason from kernel BTF, so names match the
// running kernel rather than a list compiled into netdoctor.
func dropReasons(kspec *btf.Spec) map[uint32]string {
	out := map[uint32]string{}
	var en *btf.Enum
	if kspec == nil || kspec.TypeByName("skb_drop_reason", &en) != nil {
		return out
	}
	for _, v := range en.Values {
		out[uint32(v.Value)] = strings.TrimPrefix(v.Name, "SKB_DROP_REASON_") //nolint:gosec // drop reasons are a small enum
	}
	return out
}
