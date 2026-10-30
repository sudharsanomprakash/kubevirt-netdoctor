package route

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

func podNet(binding string) topology.NetworkPath {
	return topology.NetworkPath{Name: "default", Source: topology.SourcePod, Interface: &topology.SpecInterface{Binding: binding}}
}

func multusNet(t *testing.T, name, config string, guestIPs ...string) topology.NetworkPath {
	t.Helper()
	cfg, err := multus.ParseCNIConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	n := topology.NetworkPath{
		Name: name, Source: topology.SourceMultus, Interface: &topology.SpecInterface{Binding: "bridge"},
		NAD: &topology.NADInfo{Ref: multus.NetworkRef{Namespace: "demo", Name: name}, Lookup: kube.StateFound, Config: cfg},
	}
	if len(guestIPs) > 0 {
		n.Guest = &topology.GuestInterface{Name: name, IPs: guestIPs}
	}
	return n
}

func topo(nets ...topology.NetworkPath) *topology.Topology {
	return &topology.Topology{Networks: nets}
}

const ovsHostLocal = `{"cniVersion":"0.4.0","type":"ovs","bridge":"br-data","ipam":{"type":"host-local",
	"ranges":[[{"subnet":"198.51.100.0/24","gateway":"198.51.100.1"}]],"routes":[{"dst":"203.0.113.0/24"}]}}`

func TestAnalyzeSelectsLongestPrefix(t *testing.T) {
	a := Analyze(topo(podNet("masquerade"), multusNet(t, "data", ovsHostLocal, "198.51.100.33")), netip.MustParseAddr("203.0.113.9"), nil)
	if a.Selected == nil || a.Selected.Network != "data" || a.Selected.Kind != KindRoute || a.Selected.Gateway != "198.51.100.1" {
		t.Fatalf("selected: %+v", a.Selected)
	}
	if !strings.Contains(a.Selected.GuestEvidence, "198.51.100.33 inside") {
		t.Errorf("guest evidence: %q", a.Selected.GuestEvidence)
	}
	if len(a.Candidates) != 2 || a.Candidates[1].Provenance != Inferred {
		t.Errorf("candidates: %+v", a.Candidates)
	}
	if a.Host != nil {
		t.Error("no snapshot was given")
	}

	onLink := Analyze(topo(podNet("masquerade"), multusNet(t, "data", ovsHostLocal)), netip.MustParseAddr("198.51.100.77"), nil)
	if onLink.Selected.Kind != KindOnLink || onLink.Selected.Prefix != "198.51.100.0/24" {
		t.Errorf("on-link: %+v", onLink.Selected)
	}
	if !strings.Contains(onLink.Selected.GuestEvidence, "reports no address") {
		t.Errorf("guest evidence without guest IPs: %q", onLink.Selected.GuestEvidence)
	}
}

func TestAnalyzeNoCandidatesAndBindings(t *testing.T) {
	// SR-IOV on the pod network does not get KubeVirt's default route.
	a := Analyze(topo(podNet("sriov")), netip.MustParseAddr("203.0.113.9"), nil)
	if len(a.Candidates) != 0 || a.Selected != nil || a.Ambiguous {
		t.Errorf("expected no candidates: %+v", a)
	}
	for _, b := range []string{"masquerade", "bridge", "passt", "plugin:passt"} {
		if a := Analyze(topo(podNet(b)), netip.MustParseAddr("203.0.113.9"), nil); a.Selected == nil {
			t.Errorf("%s binding: no default candidate", b)
		}
	}
}

func TestAnalyzeIPv6UsesMatchingFamily(t *testing.T) {
	a := Analyze(topo(podNet("masquerade"), multusNet(t, "data", ovsHostLocal)), netip.MustParseAddr("2001:db8::9"), nil)
	if a.Selected == nil || a.Selected.Prefix != "::/0" {
		t.Errorf("ipv6: %+v", a.Selected)
	}
}

func TestAnalyzeAmbiguous(t *testing.T) {
	cfgA := `{"type":"bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.0.2.0/24"}]]}}`
	a := Analyze(topo(multusNet(t, "a", cfgA), multusNet(t, "b", cfgA)), netip.MustParseAddr("192.0.2.5"), nil)
	if !a.Ambiguous || a.Selected != nil {
		t.Errorf("expected ambiguity: %+v", a)
	}
}

func TestAnalyzeGatewayOffLink(t *testing.T) {
	cfg := `{"type":"bridge","ipam":{"type":"host-local","ranges":[[{"subnet":"192.0.2.0/24"}]],"routes":[{"dst":"203.0.113.0/24","gw":"198.51.100.1"}]}}`
	a := Analyze(topo(multusNet(t, "a", cfg)), netip.MustParseAddr("203.0.113.1"), nil)
	if a.Selected == nil || !a.Selected.GatewayOffLink {
		t.Errorf("expected off-link gateway: %+v", a.Selected)
	}
}

func snap(routes []host.Route, ifaces ...host.Interface) *host.Snapshot {
	return &host.Snapshot{
		NodeName: "worker-1", Routes: routes, Interfaces: ifaces,
		Sources: []host.SourceStatus{{Name: host.SourceRoutes, Status: host.StatusOK}, {Name: host.SourceLinks, Status: host.StatusOK}},
	}
}

func TestHostLookup(t *testing.T) {
	dest := netip.MustParseAddr("203.0.113.9")
	eth0 := host.Interface{Name: "eth0", AdminUp: true, OperState: "UP"}
	defaultRoute := host.Route{Table: "main", Dst: "0.0.0.0/0", Gateway: "192.0.2.1", Dev: "eth0"}

	cases := []struct {
		name   string
		s      *host.Snapshot
		result string
	}{
		{"routed", snap([]host.Route{defaultRoute}, eth0), HostRouted},
		{"no route", snap([]host.Route{{Table: "main", Dst: "192.0.2.0/24", Dev: "eth0"}}, eth0), HostNoRoute},
		{"blackhole wins by prefix", snap([]host.Route{defaultRoute, {Table: "main", Dst: "203.0.113.0/24", Type: "blackhole"}}, eth0), HostRejected},
		{"local table ignored", snap([]host.Route{{Table: "local", Dst: "203.0.113.9/32", Dev: "lo", Type: "local"}}, eth0), HostNoRoute},
		{"egress down", snap([]host.Route{defaultRoute}, host.Interface{Name: "eth0", AdminUp: true, OperState: "DOWN"}), HostEgressDown},
		{"lower metric preferred", snap([]host.Route{
			{Table: "main", Dst: "0.0.0.0/0", Dev: "eth1", Metric: 200},
			{Table: "main", Dst: "0.0.0.0/0", Dev: "eth0", Metric: 100},
		}, eth0, host.Interface{Name: "eth1", AdminUp: false}), HostRouted},
	}
	for _, tc := range cases {
		a := Analyze(topo(podNet("masquerade")), dest, tc.s)
		if a.Host == nil || !a.Host.Applicable || a.Host.Result != tc.result {
			t.Errorf("%s: host = %+v", tc.name, a.Host)
		}
	}

	notCollected := snap(nil)
	notCollected.Sources = nil
	if a := Analyze(topo(podNet("masquerade")), dest, notCollected); a.Host.Result != HostUnknown {
		t.Errorf("routes not collected: %+v", a.Host)
	}
	l2 := Analyze(topo(podNet("sriov"), multusNet(t, "data", ovsHostLocal)), dest, snap([]host.Route{defaultRoute}, eth0))
	if l2.Host.Applicable {
		t.Errorf("layer-2 secondary network must not use the node routing table: %+v", l2.Host)
	}
}
