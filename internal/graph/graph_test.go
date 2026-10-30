package graph

import (
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

func baseTopology(matchedBy string) *topology.Topology {
	return &topology.Topology{
		Target:   topology.Target{Kind: topology.KindVMI, Namespace: "demo", Name: "vm"},
		Instance: &topology.VMIInfo{Name: "vm", Lookup: kube.StateFound, Phase: "Running", NodeName: "worker-1"},
		Launcher: &topology.LauncherInfo{
			Lookup:               kube.StateFound,
			Selected:             &topology.PodInfo{Name: "virt-launcher-vm", UID: "uid", NodeName: "worker-1"},
			NetworkStatusPresent: true,
		},
		NodeName: "worker-1",
		Networks: []topology.NetworkPath{{
			Name: "sec", Source: topology.SourceMultus,
			Interface:     &topology.SpecInterface{Binding: "bridge"},
			Guest:         &topology.GuestInterface{Name: "sec", InterfaceName: "enp2s0"},
			PodAttachment: &topology.PodAttachment{NetworkName: "demo/nad", Interface: "net1", MatchedBy: matchedBy},
		}},
	}
}

func edge(g *Graph, relation string) *Edge {
	for i := range g.Edges {
		if g.Edges[i].Relation == relation {
			return &g.Edges[i]
		}
	}
	return nil
}

func TestAttachmentProvenance(t *testing.T) {
	g := Build(baseTopology(topology.MatchPodInterfaceName), nil)
	if e := edge(g, "attached-via"); e == nil || e.Provenance != Observed {
		t.Errorf("podInterfaceName match must be Observed: %+v", e)
	}
	g = Build(baseTopology(topology.MatchReferenceOrder), nil)
	if e := edge(g, "attached-via"); e == nil || e.Provenance != Inferred {
		t.Errorf("reference-order match must be Inferred: %+v", e)
	}
	if e := edge(g, "bound-to"); e == nil || e.Provenance != Inferred || !strings.Contains(e.Evidence, "bridge binding") {
		t.Errorf("binding edge: %+v", e)
	}
}

func TestGapsAndAbsences(t *testing.T) {
	topo := baseTopology(topology.MatchPodInterfaceName)
	g := Build(topo, nil)
	if len(g.Gaps) != 1 || !strings.Contains(g.Gaps[0], "no host snapshot") {
		t.Errorf("gaps: %v", g.Gaps)
	}
	g = Build(topo, &hostpath.Result{SnapshotNode: "worker-9", VMINode: "worker-1"})
	if len(g.Gaps) != 1 || !strings.Contains(g.Gaps[0], `"worker-9"`) {
		t.Errorf("node mismatch gap: %v", g.Gaps)
	}

	topo.Networks[0].PodAttachment = nil
	topo.Networks[0].Guest = nil
	g = Build(topo, &hostpath.Result{NodeMatch: true, Paths: []hostpath.NetworkPath{{
		Network: "sec", Kind: hostpath.KindOVS, DeclaredDevice: "br-data", DeviceState: hostpath.Missing,
		DeviceReason: "no OVS bridge with this name in OVSDB", PodPortState: hostpath.Missing,
	}}})
	if len(g.Absent) != 4 || len(g.Gaps) != 0 {
		t.Errorf("absent=%v gaps=%v", g.Absent, g.Gaps)
	}

	noVMI := &topology.Topology{Target: topology.Target{Namespace: "demo", Name: "vm"}, Instance: &topology.VMIInfo{Lookup: kube.StateNotFound}}
	if g := Build(noVMI, nil); len(g.Gaps) != 2 {
		t.Errorf("no VMI gaps: %v", g.Gaps)
	}
}
