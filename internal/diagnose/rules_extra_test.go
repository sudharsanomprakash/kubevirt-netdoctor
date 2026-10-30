package diagnose

import (
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
)

func TestAttachmentMACMismatch(t *testing.T) {
	topo := runningTopology()
	topo.Networks[1].PodRequest.MAC = "02:00:00:00:00:01"
	topo.Networks[1].PodAttachment.MAC = "0A:58:00:00:00:99"
	f := only(t, checkAttachmentMAC(topo), IDMultusAttachmentMismatch, SeverityWarning, Suspected)
	if !strings.Contains(f.Expected, "02:00:00:00:00:01") {
		t.Errorf("expected: %q", f.Expected)
	}
	topo.Networks[1].PodAttachment.MAC = "02:00:00:00:00:01"
	if fs := checkAttachmentMAC(topo); len(fs) != 0 {
		t.Errorf("matching MACs: %v", ids(fs))
	}
}

func hostInput(paths ...hostpath.NetworkPath) *Input {
	return &Input{
		Topology: runningTopology(),
		Snapshot: &host.Snapshot{NodeName: "worker-1"},
		Host:     &hostpath.Result{SnapshotNode: "worker-1", VMINode: "worker-1", NodeMatch: true, Paths: paths},
	}
}

func TestOVSBridgeNoFlows(t *testing.T) {
	path := hostpath.NetworkPath{
		Network: "sec", Kind: hostpath.KindOVS, DeviceState: hostpath.Found,
		Device: &hostpath.DeviceView{Name: "br-data", Type: "ovs-bridge", FailMode: "secure", Flows: &host.FlowSummary{}},
	}
	only(t, checkOVSFlows(hostInput(path)), IDOVSBridgeNoFlows, SeverityWarning, Confirmed)

	path.Device.FailMode = "standalone"
	if fs := checkOVSFlows(hostInput(path)); len(fs) != 0 {
		t.Errorf("standalone bridge: %v", ids(fs))
	}
	path.Device.FailMode, path.Device.Flows = "secure", nil
	if fs := checkOVSFlows(hostInput(path)); len(fs) != 0 {
		t.Errorf("flows not collected must not be reported as empty: %v", ids(fs))
	}
}

func TestHostRulesSilentWithoutMatchingSnapshot(t *testing.T) {
	in := &Input{Topology: runningTopology()}
	for _, r := range HostRules() {
		if fs := r.Check(in); len(fs) != 0 {
			t.Errorf("%s without snapshot: %v", r.ID, ids(fs))
		}
	}
	mismatch := hostInput()
	mismatch.Host.NodeMatch = false
	fs := Evaluate(mismatch, HostRules())
	if len(fs) != 1 || fs[0].ID != IDHostSnapshotNodeMismatch {
		t.Errorf("node mismatch: %v", ids(fs))
	}
}

func TestRouteRules(t *testing.T) {
	in := func(a *route.Analysis) *Input { return &Input{Topology: runningTopology(), Route: a} }

	only(t, checkRoute(in(&route.Analysis{Destination: "203.0.113.9"})), IDRouteMissing, SeverityWarning, Suspected)

	amb := &route.Analysis{Destination: "192.0.2.5", Ambiguous: true, Candidates: []route.Candidate{{Network: "a"}, {Network: "b"}}}
	if fs := checkRoute(in(amb)); len(fs) != 1 || fs[0].ID != IDRouteAmbiguous {
		t.Errorf("ambiguous: %v", ids(fs))
	}

	sel := route.Candidate{Network: "a", Prefix: "203.0.113.0/24", Gateway: "198.51.100.1", GatewayOffLink: true}
	off := &route.Analysis{Destination: "203.0.113.9", Candidates: []route.Candidate{sel}, Selected: &sel}
	only(t, checkRoute(in(off)), IDRouteGatewayOffLink, SeverityError, Confirmed)

	ok := route.Candidate{Network: "default", Prefix: "0.0.0.0/0"}
	for result, id := range map[string]string{
		route.HostNoRoute:    IDHostRouteMissing,
		route.HostRejected:   IDHostRouteRejected,
		route.HostEgressDown: IDHostRouteEgressDown,
	} {
		a := &route.Analysis{Destination: "203.0.113.9", Candidates: []route.Candidate{ok}, Selected: &ok,
			Host: &route.HostRoute{Applicable: true, Result: result, Route: &host.Route{Dst: "0.0.0.0/0", Dev: "eth0", Type: "blackhole"}}}
		if fs := checkRoute(in(a)); len(fs) != 1 || fs[0].ID != id || fs[0].Category != Confirmed {
			t.Errorf("%s: %+v", result, fs)
		}
	}
	notApplicable := &route.Analysis{Destination: "203.0.113.9", Candidates: []route.Candidate{ok}, Selected: &ok,
		Host: &route.HostRoute{Applicable: false, Result: route.HostUnknown}}
	if fs := checkRoute(in(notApplicable)); len(fs) != 0 {
		t.Errorf("not applicable: %v", ids(fs))
	}
}
