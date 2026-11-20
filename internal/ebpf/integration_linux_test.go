//go:build linux

package ebpf

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests load the real BPF programs and generate controlled traffic
// between two network namespaces joined by a veth pair. They need root and
// a kernel with BTF; otherwise they are skipped. Set NETDOCTOR_BPF_TESTS=0
// to skip them explicitly.

const (
	addrA = "10.250.0.1"
	addrB = "10.250.0.2"
)

type testNet struct {
	nsA, nsB string // /run/netns paths
	nameA    string
	nameB    string
}

func requireBPF(t *testing.T) {
	t.Helper()
	if os.Getenv("NETDOCTOR_BPF_TESTS") == "0" {
		t.Skip("NETDOCTOR_BPF_TESTS=0")
	}
	if os.Geteuid() != 0 {
		t.Skip("BPF integration tests need root")
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		t.Skip("kernel BTF not available")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("ip(8) not available")
	}
}

func run(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func setupNet(t *testing.T) *testNet {
	t.Helper()
	suffix := fmt.Sprintf("%d", os.Getpid()%100000)
	n := &testNet{nameA: "nda" + suffix, nameB: "ndb" + suffix}
	n.nsA, n.nsB = "/run/netns/"+n.nameA, "/run/netns/"+n.nameB
	run(t, "ip", "netns", "add", n.nameA)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", n.nameA).Run() })
	run(t, "ip", "netns", "add", n.nameB)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", n.nameB).Run() })
	run(t, "ip", "link", "add", "va", "netns", n.nameA, "type", "veth", "peer", "name", "vb", "netns", n.nameB)
	run(t, "ip", "-n", n.nameA, "addr", "add", addrA+"/24", "dev", "va")
	run(t, "ip", "-n", n.nameB, "addr", "add", addrB+"/24", "dev", "vb")
	run(t, "ip", "-n", n.nameA, "link", "set", "va", "up")
	run(t, "ip", "-n", n.nameB, "link", "set", "vb", "up")
	run(t, "ip", "-n", n.nameA, "link", "set", "lo", "up")
	run(t, "ip", "-n", n.nameB, "link", "set", "lo", "up")
	return n
}

func (n *testNet) ifaceB(t *testing.T) InterfaceFilter {
	t.Helper()
	f, err := ResolveInterface(n.nsB, "vb")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// dialIn creates a socket inside a namespace; it keeps that namespace.
func dialIn(t *testing.T, ns, network, addr string, timeout time.Duration) (net.Conn, error) {
	t.Helper()
	var c net.Conn
	err := inNetns(ns, func() error {
		var e error
		c, e = net.DialTimeout(network, addr, timeout)
		return e
	})
	return c, err
}

func listenIn(t *testing.T, ns, addr string) net.Listener {
	t.Helper()
	var l net.Listener
	if err := inNetns(ns, func() error {
		var e error
		l, e = net.Listen("tcp4", addr)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return l
}

func traceWith(t *testing.T, opts Options, traffic func()) *Trace {
	t.Helper()
	done := make(chan struct{})
	opts.ready = func() {
		go func() {
			defer close(done)
			traffic()
		}()
	}
	tr, err := Run(context.Background(), opts, Meta{NodeName: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	<-done
	return tr
}

func findEvents(tr *Trace, typ string) []Event {
	var out []Event
	for _, e := range tr.Events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestTraceUDPClosedPortDrop(t *testing.T) {
	requireBPF(t)
	n := setupNet(t)
	vb := n.ifaceB(t)
	tr := traceWith(t, Options{
		Duration:   1500 * time.Millisecond,
		Events:     []string{GroupDrops, GroupInterfaces},
		Interfaces: []InterfaceFilter{vb},
	}, func() {
		// Unconnected, so ICMP port-unreachable does not fail later sends.
		var pc net.PacketConn
		if err := inNetns(n.nsA, func() error {
			var e error
			pc, e = net.ListenPacket("udp4", addrA+":0")
			return e
		}); err != nil {
			t.Error(err)
			return
		}
		defer pc.Close()
		dst := &net.UDPAddr{IP: net.ParseIP(addrB), Port: 9999}
		for i := 0; i < 3; i++ {
			if _, err := pc.WriteTo([]byte("netdoctor"), dst); err != nil {
				t.Error(err)
			}
		}
	})
	var hit *Event
	for _, e := range findEvents(tr, TypeDrop) {
		if e.Protocol == "udp" && e.DstPort == 9999 {
			e := e
			hit = &e
			break
		}
	}
	if hit == nil {
		t.Fatalf("no UDP drop to port 9999 observed; events=%+v counters=%+v", tr.Events, tr.Counters)
	}
	if hit.DropReason != "NO_SOCKET" {
		t.Errorf("drop reason = %q, want NO_SOCKET", hit.DropReason)
	}
	if hit.Src != addrA || hit.Dst != addrB || hit.Interface != "" && hit.Interface != "vb" {
		t.Errorf("unexpected drop %+v", *hit)
	}
	if hit.IfIndex != vb.Index || hit.NetnsID != vb.NetnsID {
		t.Errorf("drop attributed to %d/%d, want vb %d/%d", hit.NetnsID, hit.IfIndex, vb.NetnsID, vb.Index)
	}
	if len(hit.MatchedBy) != 1 || hit.MatchedBy[0] != "interface" {
		t.Errorf("matchedBy = %v", hit.MatchedBy)
	}
	if !strings.Contains(hit.Location, "udp") && !strings.HasPrefix(hit.Location, "0x") {
		t.Errorf("drop location %q is not in the UDP stack", hit.Location)
	}
	if tr.Counters.Drops < 3 {
		t.Errorf("drop counter = %d, want >= 3", tr.Counters.Drops)
	}
	var rx bool
	for _, ic := range tr.Ifaces {
		if ic.Index == vb.Index && ic.Direction == "rx" && ic.Packets >= 3 {
			rx = true
		}
	}
	if !rx {
		t.Errorf("no rx interface counts for vb: %+v", tr.Ifaces)
	}
	if !tr.Overhead.Measured || tr.Overhead.TotalRuns == 0 {
		t.Errorf("overhead not measured: %+v", tr.Overhead)
	}
	t.Logf("overhead: %d runs, %.0f ns/run, %.4f%% of one core", tr.Overhead.TotalRuns, tr.Overhead.AvgNsPerRun, tr.Overhead.CPUPercentOne)
}

func TestTraceTCPResetToClosedPort(t *testing.T) {
	requireBPF(t)
	n := setupNet(t)
	tr := traceWith(t, Options{
		Duration:  1500 * time.Millisecond,
		Events:    []string{GroupTCP, GroupDrops},
		Addresses: []netip.Addr{netip.MustParseAddr(addrB)},
	}, func() {
		_, err := dialIn(t, n.nsA, "tcp4", addrB+":7", time.Second)
		if err == nil {
			t.Error("connect to a closed port succeeded")
		}
	})
	sent := findEvents(tr, TypeTCPResetSent)
	recv := findEvents(tr, TypeTCPResetRecv)
	if len(sent) == 0 || len(recv) == 0 {
		t.Fatalf("want reset sent and received; sent=%v recv=%v counters=%+v", sent, recv, tr.Counters)
	}
	if sent[0].Src != addrB || sent[0].SrcPort != 7 || sent[0].Dst != addrA {
		t.Errorf("reset sent %+v: want from %s:7 to %s", sent[0], addrB, addrA)
	}
	if recv[0].Dst != addrB || recv[0].DstPort != 7 {
		t.Errorf("reset received %+v: want peer %s:7", recv[0], addrB)
	}
	var synSent bool
	for _, e := range findEvents(tr, TypeTCPStateChange) {
		if e.NewState == "SYN_SENT" && e.DstPort == 7 {
			synSent = true
		}
	}
	if !synSent {
		t.Error("no SYN_SENT state transition observed")
	}
}

func TestTraceTCPRetransmitWhenPeerLinkDown(t *testing.T) {
	requireBPF(t)
	n := setupNet(t)
	l := listenIn(t, n.nsB, addrB+":8080")
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(5 * time.Second)
		}
	}()
	c, err := dialIn(t, n.nsA, "tcp4", addrB+":8080", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tr := traceWith(t, Options{
		Duration:  2500 * time.Millisecond,
		Events:    []string{GroupTCP},
		Addresses: []netip.Addr{netip.MustParseAddr(addrB)},
	}, func() {
		run(t, "ip", "-n", n.nameB, "link", "set", "vb", "down")
		_, _ = c.Write([]byte("payload that cannot be delivered"))
	})
	re := findEvents(tr, TypeTCPRetransmit)
	if len(re) == 0 {
		t.Fatalf("no retransmissions observed; counters=%+v", tr.Counters)
	}
	if re[0].Dst != addrB || re[0].DstPort != 8080 || re[0].Src != addrA {
		t.Errorf("retransmit %+v", re[0])
	}
	if tr.Counters.TCPRetransmits != uint64(len(re)) {
		t.Errorf("counter %d != events %d", tr.Counters.TCPRetransmits, len(re))
	}
}

func TestTraceLostEventsAreCounted(t *testing.T) {
	requireBPF(t)
	n := setupNet(t)
	vb := n.ifaceB(t)
	const sent = 20000
	tr := traceWith(t, Options{
		Duration:   2 * time.Second,
		Events:     []string{GroupDrops},
		Interfaces: []InterfaceFilter{vb},
		RingBytes:  MinRingBytes, // ~40 events fit
		MaxEvents:  5,
	}, func() {
		c, err := dialIn(t, n.nsA, "udp4", addrB+":9998", time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		for i := 0; i < sent; i++ {
			_, _ = c.Write([]byte("x"))
		}
	})
	if tr.Counters.LostEvents == 0 {
		t.Errorf("no lost events with a %d-byte ring buffer; counters=%+v", MinRingBytes, tr.Counters)
	}
	if len(tr.Events) != 5 {
		t.Errorf("recorded %d events, want MaxEvents=5", len(tr.Events))
	}
	if tr.Counters.SuppressedEvents == 0 {
		t.Error("suppressed events not counted")
	}
	total := uint64(len(tr.Events)) + tr.Counters.SuppressedEvents + tr.Counters.LostEvents
	if total != tr.Counters.Drops {
		t.Errorf("recorded+suppressed+lost = %d, kernel drop counter = %d", total, tr.Counters.Drops)
	}
	if len(tr.Limitations) == 0 {
		t.Error("lost events must be reported as a limitation")
	}
	t.Logf("drops=%d lost=%d suppressed=%d", tr.Counters.Drops, tr.Counters.LostEvents, tr.Counters.SuppressedEvents)
}

func TestTraceFilterExcludesOtherTraffic(t *testing.T) {
	requireBPF(t)
	n := setupNet(t)
	tr := traceWith(t, Options{
		Duration:  1 * time.Second,
		Events:    []string{GroupDrops},
		Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.77")},
	}, func() {
		c, err := dialIn(t, n.nsA, "udp4", addrB+":9997", time.Second)
		if err == nil {
			defer c.Close()
			_, _ = c.Write([]byte("x"))
		}
	})
	for _, e := range tr.Events {
		if e.DstPort == 9997 {
			t.Fatalf("filtered-out traffic was recorded: %+v", e)
		}
	}
}
