package hostpath

import (
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

const podUID = "pod-uid"

func withNAD(t *testing.T, config, podIface string) *topology.Topology {
	t.Helper()
	cfg, err := multus.ParseCNIConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	n := topology.NetworkPath{
		Name: "sec", Source: topology.SourceMultus,
		NAD: &topology.NADInfo{Ref: multus.NetworkRef{Namespace: "demo", Name: "sec"}, Lookup: kube.StateFound, Config: cfg},
	}
	if podIface != "" {
		n.PodAttachment = &topology.PodAttachment{Interface: podIface}
	}
	return &topology.Topology{
		NodeName: "worker-1",
		Launcher: &topology.LauncherInfo{Selected: &topology.PodInfo{Name: "virt-launcher", UID: podUID}},
		Networks: []topology.NetworkPath{n},
	}
}

func ok(names ...string) []host.SourceStatus {
	var out []host.SourceStatus
	for _, n := range names {
		out = append(out, host.SourceStatus{Name: n, Status: host.StatusOK})
	}
	return out
}

func only(t *testing.T, r *Result) NetworkPath {
	t.Helper()
	if r == nil || !r.NodeMatch || len(r.Paths) != 1 {
		t.Fatalf("result: %+v", r)
	}
	return r.Paths[0]
}

func TestCorrelateNilAndNodeMismatch(t *testing.T) {
	topo := withNAD(t, `{"type":"bridge","bridge":"br1"}`, "")
	if Correlate(topo, nil) != nil {
		t.Error("nil snapshot must give nil result")
	}
	r := Correlate(topo, &host.Snapshot{NodeName: "worker-2"})
	if r.NodeMatch || len(r.Paths) != 0 {
		t.Errorf("node mismatch: %+v", r)
	}
}

func TestLinuxBridgeDefaultNameAndWrongKind(t *testing.T) {
	topo := withNAD(t, `{"type":"bridge"}`, "")
	s := &host.Snapshot{NodeName: "worker-1", Sources: ok(host.SourceLinks), Interfaces: []host.Interface{{Name: "cni0", Kind: "dummy"}}}
	p := only(t, Correlate(topo, s))
	if p.DeclaredDevice != "cni0" || p.DeviceState != Missing || !strings.Contains(p.DeviceReason, "not a Linux bridge") {
		t.Errorf("path: %+v", p)
	}

	s.Sources = nil
	if p := only(t, Correlate(topo, s)); p.DeviceState != Unknown {
		t.Errorf("links not collected must be Unknown: %+v", p)
	}
}

func TestMacvlanMaster(t *testing.T) {
	topo := withNAD(t, `{"type":"macvlan","master":"bond0.50","mtu":9000}`, "")
	s := &host.Snapshot{NodeName: "worker-1", Sources: ok(host.SourceLinks), Interfaces: []host.Interface{
		{Name: "bond0.50", Kind: "vlan", Parent: "bond0", MTU: 1500, AdminUp: true, OperState: "UP", VLAN: &host.VLANInfo{ID: 50}},
		{Name: "bond0", Kind: "bond", MTU: 1500, AdminUp: true, OperState: "UP", Bond: &host.BondInfo{Mode: "active-backup"}},
		{Name: "eno1", Master: "bond0", MTU: 1500, BondMember: &host.BondMemberInfo{MIIStatus: "UP"}},
		{Name: "eno2", Master: "bond0", MTU: 1500, BondMember: &host.BondMemberInfo{MIIStatus: "DOWN"}},
	}}
	p := only(t, Correlate(topo, s))
	if p.Kind != KindMacvlan || p.DeviceState != Found || len(p.Uplinks) != 1 {
		t.Fatalf("path: %+v", p)
	}
	u := p.Uplinks[0]
	if u.Type != "vlan 50" || len(u.Lower) != 1 || u.Lower[0].Bond == nil {
		t.Fatalf("uplink chain: %+v", u)
	}
	bonds := p.Bonds()
	if len(bonds) != 1 || !bonds[0].Members[1].Detached || bonds[0].Members[0].Detached {
		t.Errorf("bond members: %+v", bonds)
	}
	var names []string
	p.Walk(func(u *Uplink) { names = append(names, u.Name) })
	if strings.Join(names, ",") != "bond0.50,bond0" {
		t.Errorf("walk = %v", names)
	}

	noMaster := withNAD(t, `{"type":"macvlan"}`, "")
	if p := only(t, Correlate(noMaster, s)); p.DeviceState != Unknown || !strings.Contains(p.DeviceReason, "default-route") {
		t.Errorf("macvlan without master: %+v", p)
	}
}

func ovsSnapshot(ports ...host.OVSPort) *host.Snapshot {
	return &host.Snapshot{
		NodeName: "worker-1",
		Sources:  ok(host.SourceLinks, host.SourceOVSDB),
		OVS: &host.OVS{
			Bridges: []host.OVSBridge{{Name: "br-data"}, {Name: "br-other"}},
			Ports:   ports,
			Interfaces: []host.OVSInterface{
				{Name: "ens1f0", LinkState: "up"}, {Name: "ens1f1", LinkState: "down"},
			},
		},
	}
}

func cniPort(name, bridge, iface string) host.OVSPort {
	return host.OVSPort{Name: name, Bridge: bridge, ExternalIDs: map[string]string{
		host.ExternalIDOwner: host.OVSCNIOwner, host.ExternalIDPodUID: podUID, host.ExternalIDContIface: iface,
	}}
}

func TestOVSPodPortStates(t *testing.T) {
	cfg := `{"type":"ovs","bridge":"br-data","vlan":200}`

	p := only(t, Correlate(withNAD(t, cfg, "net1"), ovsSnapshot(cniPort("vethA", "br-other", "net1"))))
	if p.PodPortState != WrongBridge || p.PodPort.Bridge != "br-other" {
		t.Errorf("wrong bridge: %+v", p)
	}

	// Two ports of the same pod and no interface name to disambiguate.
	p = only(t, Correlate(withNAD(t, cfg, ""), ovsSnapshot(cniPort("vethA", "br-data", "net1"), cniPort("vethB", "br-data", "net2"))))
	if p.PodPortState != Unknown || p.PodPort != nil {
		t.Errorf("ambiguous: %+v", p)
	}

	// A port with another owner is never attributed to the VM.
	foreign := cniPort("vethX", "br-data", "net1")
	foreign.ExternalIDs[host.ExternalIDOwner] = "someone-else"
	if p := only(t, Correlate(withNAD(t, cfg, "net1"), ovsSnapshot(foreign))); p.PodPortState != Missing {
		t.Errorf("foreign port: %+v", p)
	}

	noPod := withNAD(t, cfg, "net1")
	noPod.Launcher = nil
	if p := only(t, Correlate(noPod, ovsSnapshot())); p.PodPortState != Unknown {
		t.Errorf("no launcher: %+v", p)
	}
}

func TestOVSBondWithoutRuntimeState(t *testing.T) {
	s := ovsSnapshot(host.OVSPort{Name: "bond1", Bridge: "br-data", Interfaces: []string{"ens1f0", "ens1f1"}, BondMode: "active-backup"})
	s.OVS.Bridges[0].Ports = []string{"bond1"}
	p := only(t, Correlate(withNAD(t, `{"type":"ovs","bridge":"br-data"}`, ""), s))
	if len(p.Uplinks) != 1 || p.Uplinks[0].Bond == nil {
		t.Fatalf("uplinks: %+v", p.Uplinks)
	}
	b := p.Uplinks[0].Bond
	if b.RuntimeObserved || b.Members[0].Detached || !b.Members[1].Detached || b.Members[1].Reason != "link state down" {
		t.Errorf("bond: %+v", b)
	}
	if p.Uplinks[0].VLANInfo != "no tag or trunks (all VLANs)" {
		t.Errorf("vlan info: %q", p.Uplinks[0].VLANInfo)
	}
}

func TestOVSNotInstalledOrNotCollected(t *testing.T) {
	topo := withNAD(t, `{"type":"ovs","bridge":"br-data"}`, "net1")
	s := &host.Snapshot{NodeName: "worker-1", Sources: []host.SourceStatus{{Name: host.SourceOVSDB, Status: host.StatusNotInstalled}}}
	if p := only(t, Correlate(topo, s)); p.DeviceState != Missing || !strings.Contains(p.DeviceReason, "not installed") {
		t.Errorf("not installed: %+v", p)
	}
	s.Sources[0].Status = host.StatusFailed
	if p := only(t, Correlate(topo, s)); p.DeviceState != Unknown || p.PodPortState != Unknown {
		t.Errorf("failed collection: %+v", p)
	}
}

func TestNotModeledAndUnresolved(t *testing.T) {
	if p := only(t, Correlate(withNAD(t, `{"type":"sriov"}`, ""), &host.Snapshot{NodeName: "worker-1"})); p.Kind != KindNotModeled {
		t.Errorf("sriov: %+v", p)
	}
	topo := withNAD(t, `{"type":"bridge"}`, "")
	topo.Networks[0].NAD.Config = nil
	if p := only(t, Correlate(topo, &host.Snapshot{NodeName: "worker-1"})); p.Kind != KindUnresolved {
		t.Errorf("unresolved: %+v", p)
	}
}
