package ebpf

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	a := netip.MustParseAddr("192.0.2.1")
	cases := []struct {
		name string
		opts Options
		err  string
	}{
		{"defaults with address filter", Options{Addresses: []netip.Addr{a}}, ""},
		{"no filter", Options{}, "a filter is required"},
		{"explicit unfiltered", Options{Unfiltered: true}, ""},
		{"too long", Options{Unfiltered: true, Duration: time.Hour}, "outside"},
		{"negative", Options{Unfiltered: true, Duration: -time.Second}, "outside"},
		{"bad group", Options{Unfiltered: true, Events: []string{"dns"}}, "unknown event group"},
		{"ring not power of two", Options{Unfiltered: true, RingBytes: 5000}, "power of two"},
		{"ring too small", Options{Unfiltered: true, RingBytes: 1024}, "power of two"},
		{"negative max events", Options{Unfiltered: true, MaxEvents: -1}, "positive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.opts.Validate()
			if c.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c.opts.Duration != DefaultDuration || c.opts.MaxEvents != DefaultMaxEvents || c.opts.RingBytes != DefaultRingBytes || len(c.opts.Events) != 3 {
					t.Errorf("defaults not applied: %+v", c.opts)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want %q", err, c.err)
			}
		})
	}
}

func TestAddressConversion(t *testing.T) {
	v4 := netip.MustParseAddr("10.1.2.3")
	k := AddrKey(v4)
	if got := addrFrom(k, 2); got != "10.1.2.3" {
		t.Errorf("v4 round trip = %q", got)
	}
	v6 := netip.MustParseAddr("2001:db8::1")
	if got := addrFrom(AddrKey(v6), 10); got != "2001:db8::1" {
		t.Errorf("v6 round trip = %q", got)
	}
	if got := addrFrom(k, 0); got != "" {
		t.Errorf("unknown family gave %q", got)
	}
	if got := addrFrom([16]uint8{}, 10); got != "" {
		t.Errorf("unspecified gave %q", got)
	}
}

func TestNames(t *testing.T) {
	if TCPStateName(1) != "ESTABLISHED" || TCPStateName(99) != "STATE_99" {
		t.Error("TCP state names")
	}
	if ProtocolName(17) != "udp" || ProtocolName(0) != "" || ProtocolName(132) != "ip-proto-132" {
		t.Error("protocol names")
	}
}

func TestKallsyms(t *testing.T) {
	in := `ffffffff81000000 T _stext
ffffffff81200000 t udp_queue_rcv_one_skb
ffffffff81200400 T __udp4_lib_rcv
0000000000000000 T hidden
ffffffff81300000 D some_data
`
	k, err := parseKallsyms(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := k.resolve(0xffffffff81200510); got != "__udp4_lib_rcv+0x110" {
		t.Errorf("resolve = %q", got)
	}
	if got := k.resolve(0x10); got != "0x10" {
		t.Errorf("below first symbol = %q", got)
	}
	if _, err := parseKallsyms(strings.NewReader("0000000000000000 T x\n")); err == nil {
		t.Error("all-zero addresses (kptr_restrict) must be an error")
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
