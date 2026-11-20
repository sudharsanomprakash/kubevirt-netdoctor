//go:build linux

package ebpf

import (
	"reflect"
	"testing"
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
