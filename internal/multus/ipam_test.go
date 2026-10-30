package multus

import (
	"reflect"
	"strings"
	"testing"
)

func primaryIPAM(t *testing.T, raw string) *IPAM {
	t.Helper()
	cfg, err := ParseCNIConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.Primary()
	if p == nil || p.IPAMConfig == nil {
		t.Fatalf("no IPAM config parsed: %+v", cfg)
	}
	return p.IPAMConfig
}

func TestIPAMHostLocal(t *testing.T) {
	got := primaryIPAM(t, `{"cniVersion":"0.3.1","type":"bridge","bridge":"br1","ipam":{
		"type":"host-local",
		"ranges":[[{"subnet":"192.0.2.0/24","gateway":"192.0.2.1"}],[{"subnet":"2001:db8::/64"}]],
		"routes":[{"dst":"0.0.0.0/0"},{"dst":"198.51.100.7/24","gw":"192.0.2.254"}]}}`)
	want := &IPAM{
		Type:     "host-local",
		Subnets:  []string{"192.0.2.0/24", "2001:db8::/64"},
		Gateways: []string{"192.0.2.1"},
		Routes:   []Route{{Dst: "0.0.0.0/0"}, {Dst: "198.51.100.0/24", GW: "192.0.2.254"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestIPAMHostLocalLegacySubnet(t *testing.T) {
	got := primaryIPAM(t, `{"cniVersion":"0.3.1","type":"bridge","ipam":{"type":"host-local","subnet":"203.0.113.0/24","gateway":"203.0.113.1"}}`)
	if !reflect.DeepEqual(got.Subnets, []string{"203.0.113.0/24"}) || !reflect.DeepEqual(got.Gateways, []string{"203.0.113.1"}) {
		t.Errorf("got %+v", got)
	}
}

func TestIPAMStatic(t *testing.T) {
	got := primaryIPAM(t, `{"cniVersion":"0.3.1","type":"ovs","bridge":"br-data","ipam":{"type":"static",
		"addresses":[{"address":"198.51.100.10/24","gateway":"198.51.100.1"}]}}`)
	if !reflect.DeepEqual(got.Addresses, []string{"198.51.100.10/24"}) ||
		!reflect.DeepEqual(got.Subnets, []string{"198.51.100.0/24"}) ||
		!reflect.DeepEqual(got.Gateways, []string{"198.51.100.1"}) {
		t.Errorf("got %+v", got)
	}
}

func TestIPAMWhereabouts(t *testing.T) {
	got := primaryIPAM(t, `{"cniVersion":"0.3.1","type":"macvlan","master":"eth1","ipam":{"type":"whereabouts",
		"range":"192.0.2.225-192.0.2.230/24","ipRanges":[{"range":"2001:db8:1::/64"}],"gateway":"192.0.2.1"}}`)
	if !reflect.DeepEqual(got.Subnets, []string{"192.0.2.0/24", "2001:db8:1::/64"}) || !reflect.DeepEqual(got.Gateways, []string{"192.0.2.1"}) {
		t.Errorf("got %+v", got)
	}
}

func TestIPAMWhereaboutsUnparseableIsWarning(t *testing.T) {
	cfg, err := ParseCNIConfig(`{"cniVersion":"0.3.1","type":"macvlan","ipam":{"type":"whereabouts","range":"not-a-range"}}`)
	if err != nil {
		t.Fatalf("whereabouts problems must not be fatal: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "could not be parsed") {
		t.Errorf("warnings = %q", cfg.Warnings)
	}
}

func TestIPAMDHCPIsDynamic(t *testing.T) {
	if got := primaryIPAM(t, `{"cniVersion":"0.3.1","type":"macvlan","ipam":{"type":"dhcp"}}`); !got.Dynamic || len(got.Subnets) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestIPAMInvalid(t *testing.T) {
	for name, raw := range map[string]string{
		"bad subnet":         `{"type":"bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.0.2.0/33"}]]}}`,
		"gateway outside":    `{"type":"bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.0.2.0/24","gateway":"198.51.100.1"}]]}}`,
		"static no prefix":   `{"type":"bridge","ipam":{"type":"static","addresses":[{"address":"192.0.2.5"}]}}`,
		"route bad dst":      `{"type":"bridge","ipam":{"type":"host-local","routes":[{"dst":"default"}]}}`,
		"route bad gw":       `{"type":"bridge","ipam":{"type":"host-local","routes":[{"dst":"0.0.0.0/0","gw":"x"}]}}`,
		"ranges wrong shape": `{"type":"bridge","ipam":{"type":"host-local","ranges":[{"subnet":"192.0.2.0/24"}]}}`,
		"static wrong shape": `{"type":"bridge","ipam":{"type":"static","addresses":"192.0.2.5/24"}}`,
	} {
		if _, err := ParseCNIConfig(raw); err == nil || !strings.Contains(err.Error(), "ipam") {
			t.Errorf("%s: error = %v, want an ipam error", name, err)
		}
	}
}

func TestTrunkParsing(t *testing.T) {
	cfg, err := ParseCNIConfig(`{"cniVersion":"0.4.0","type":"ovs","bridge":"br-data","trunk":[{"minID":200,"maxID":210},{"id":42}]}`)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Primary()
	if !reflect.DeepEqual(p.TrunkVLANs, []VLANRange{{42, 42}, {200, 210}}) {
		t.Errorf("TrunkVLANs = %+v", p.TrunkVLANs)
	}
	if !p.AllowsVLAN(205) || !p.AllowsVLAN(42) || p.AllowsVLAN(100) {
		t.Error("AllowsVLAN mismatch")
	}

	for name, raw := range map[string]string{
		"missing ids":  `{"type":"bridge","vlanTrunk":[{"minID":5}]}`,
		"out of range": `{"type":"bridge","vlanTrunk":[{"id":4095}]}`,
		"reversed":     `{"type":"ovs","trunk":[{"minID":20,"maxID":10}]}`,
		"not a list":   `{"type":"ovs","trunk":{"id":1}}`,
	} {
		if _, err := ParseCNIConfig(raw); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
