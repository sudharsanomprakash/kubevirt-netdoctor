package traceplan

import (
	"testing"
	"time"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

func topo() *topology.Topology {
	return &topology.Topology{
		Target:   topology.Target{Kind: topology.KindVM, Namespace: "demo", Name: "web"},
		Instance: &topology.VMIInfo{Name: "web", UID: "vmi-uid"},
		Launcher: &topology.LauncherInfo{Selected: &topology.PodInfo{Name: "virt-launcher-web-x", UID: "pod-uid", NodeName: "worker-1", PodIPs: []string{"10.244.1.5"}}},
		NodeName: "worker-1",
		Networks: []topology.NetworkPath{
			{Name: "default", Interface: &topology.SpecInterface{Binding: "masquerade"}, Guest: &topology.GuestInterface{IPs: []string{"10.0.2.2", "10.244.1.5", "fe80::1"}}},
			{Name: "vlan100", Interface: &topology.SpecInterface{Binding: "bridge"},
				PodAttachment: &topology.PodAttachment{IPs: []string{"192.0.2.10"}},
				Guest:         &topology.GuestInterface{IPs: []string{"192.0.2.10", "not-an-ip"}}},
		},
	}
}

func TestBuild(t *testing.T) {
	p, err := Build(topo(), []string{"drops"}, 30*time.Second, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if p.NodeName != "worker-1" || p.PodUID != "pod-uid" || p.Target.PodUID != "pod-uid" || p.VMIName != "web" {
		t.Errorf("plan identity %+v", p)
	}
	want := map[string]bool{"10.0.2.2": true, "10.244.1.5": false, "192.0.2.10": false, "fe80::1": true}
	if len(p.Addresses) != len(want) {
		t.Fatalf("addresses %+v", p.Addresses)
	}
	for _, a := range p.Addresses {
		amb, ok := want[a.IP]
		if !ok || amb != a.Ambiguous {
			t.Errorf("address %+v: want ambiguous=%v", a, amb)
		}
	}
	if err := p.Validate(); err != nil {
		t.Error(err)
	}
	if len(p.Filters()) != 4 {
		t.Errorf("filters %v", p.Filters())
	}
}

func TestBuildErrors(t *testing.T) {
	cases := map[string]func(*topology.Topology){
		"no VMI":        func(t *topology.Topology) { t.Instance = nil },
		"no launcher":   func(t *topology.Topology) { t.Launcher.Selected = nil },
		"not scheduled": func(t *topology.Topology) { t.NodeName = ""; t.Launcher.Selected.NodeName = "" },
	}
	for name, mut := range cases {
		tp := topo()
		mut(tp)
		if _, err := Build(tp, nil, time.Second, time.Now()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestAttribute(t *testing.T) {
	addrs := []Address{{IP: "192.0.2.10"}, {IP: "10.0.2.2", Ambiguous: true}}
	tr := &ebpf.Trace{Events: []ebpf.Event{
		{Type: ebpf.TypeDrop, NetnsID: 42, MatchedBy: []string{"interface"}, DropReason: "NO_SOCKET"},
		{Type: ebpf.TypeDrop, NetnsID: 7, MatchedBy: []string{"interface"}},
		{Type: ebpf.TypeTCPRetransmit, Src: "198.51.100.1", Dst: "192.0.2.10", MatchedBy: []string{"address"}},
		{Type: ebpf.TypeTCPResetSent, Src: "10.0.2.2", Dst: "198.51.100.1", MatchedBy: []string{"address"}},
		{Type: ebpf.TypeDrop, MatchedBy: []string{"unfiltered"}},
		{Type: ebpf.TypeDrop, Src: "10.0.2.2", Dst: "192.0.2.10", MatchedBy: []string{"address"}, DropReason: "NETFILTER_DROP"},
	}}
	Attribute(tr, 42, addrs)
	want := []string{DirectlyAttributed, Unattributed, Correlated, Ambiguous, Unattributed, Correlated}
	for i, w := range want {
		if tr.Events[i].Attribution != w {
			t.Errorf("event %d attribution = %s, want %s", i, tr.Events[i].Attribution, w)
		}
	}
	s := Summarize(tr)
	if s.DropReasons["NO_SOCKET"] != 1 || s.DropReasons["NETFILTER_DROP"] != 1 || s.Retransmits != 1 || s.ResetsSent != 0 || s.AmbiguousOnly != 1 {
		t.Errorf("summary %+v", s)
	}
}
