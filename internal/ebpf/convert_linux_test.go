//go:build linux

package ebpf

import (
	"reflect"
	"testing"

	"github.com/cilium/ebpf/btf"
)

func TestMatchedBy(t *testing.T) {
	cases := map[uint8][]string{
		0:    {},
		1:    {"interface"},
		3:    {"interface", "address"},
		0x80: {"unfiltered"},
	}
	for bits, want := range cases {
		if got := matchedBy(bits); !reflect.DeepEqual(got, want) {
			t.Errorf("matchedBy(%#x) = %v, want %v", bits, got, want)
		}
	}
}

func TestConvertEvent(t *testing.T) {
	c := &converter{
		reasons:  map[uint32]string{2: "NO_SOCKET"},
		names:    map[netdoctorIfkey]string{{Netns: 7, Ifindex: 3}: "tap0"},
		symsDone: true,
	}
	raw := netdoctorEvent{Type: evDrop, Ifindex: 3, Netns: 7, Reason: 2, Family: 2, Proto: 17, Sport: 1000, Dport: 53, Matched: 1}
	raw.Saddr = AddrKey(mustAddr("10.0.0.1"))
	raw.Daddr = AddrKey(mustAddr("10.0.0.2"))
	e := c.event(&raw, clock{})
	if e.Type != TypeDrop || e.DropReason != "NO_SOCKET" || e.Interface != "tap0" || e.Protocol != "udp" ||
		e.Src != "10.0.0.1" || e.Dst != "10.0.0.2" || e.DstPort != 53 {
		t.Errorf("unexpected %+v", e)
	}
	raw = netdoctorEvent{Type: evTCPState, Oldstate: 2, Newstate: 1}
	if e := c.event(&raw, clock{}); e.OldState != "SYN_SENT" || e.NewState != "ESTABLISHED" {
		t.Errorf("state %+v", e)
	}
	raw = netdoctorEvent{Type: evDrop, Reason: 77}
	if e := c.event(&raw, clock{}); e.DropReason != "REASON_77" {
		t.Errorf("unknown reason %q", e.DropReason)
	}
}

func TestNonDropReasonsAcrossKernels(t *testing.T) {
	// 5.17: no non-drop values; 1 is NO_SOCKET and must not be skipped.
	old := &btf.Enum{Values: []btf.EnumValue{{Name: "SKB_DROP_REASON_NOT_SPECIFIED", Value: 0}, {Name: "SKB_DROP_REASON_NO_SOCKET", Value: 1}}}
	if nd, c := nonDropReasonsFrom(old); nd != ^uint32(0) || c != ^uint32(0) {
		t.Errorf("5.17 layout: skip %d/%d, want none", nd, c)
	}
	// 6.1+: NOT_DROPPED_YET=0, CONSUMED=1.
	cur := &btf.Enum{Values: []btf.EnumValue{{Name: "SKB_NOT_DROPPED_YET", Value: 0}, {Name: "SKB_CONSUMED", Value: 1}, {Name: "SKB_DROP_REASON_NOT_SPECIFIED", Value: 2}}}
	if nd, c := nonDropReasonsFrom(cur); nd != 0 || c != 1 {
		t.Errorf("6.1 layout: skip %d/%d, want 0/1", nd, c)
	}
	if nd, c := nonDropReasons(nil); nd != ^uint32(0) || c != ^uint32(0) {
		t.Error("no BTF must skip nothing")
	}
}
