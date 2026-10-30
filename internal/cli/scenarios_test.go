package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
)

// snapshotFile loads a baseline host snapshot, applies one mutation that
// introduces the fault under test, and writes it to a temporary file.
func snapshotFile(t *testing.T, base string, mutate func(*host.Snapshot)) string {
	t.Helper()
	s, err := host.LoadSnapshot(filepath.Join("..", "..", "testdata", "hosts", base))
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(s)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ovsPort(s *host.Snapshot, name string) *host.OVSPort { return s.OVS.FindPort(name) }

func setSource(s *host.Snapshot, name, status, msg string) {
	for i := range s.Sources {
		if s.Sources[i].Name == name {
			s.Sources[i].Status, s.Sources[i].Message = status, msg
		}
	}
}

func intp(v int) *int { return &v }

func TestDiagnoseScenarios(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		snapshot string
		mutate   func(*host.Snapshot)
		args     []string
		wantIDs  []string
		golden   string
	}{
		{
			name: "pod network only, route via node", fixture: "pod-network-only.yaml", snapshot: "worker-3-pod-network.json",
			args: []string{"vm/web-04", "--destination", "203.0.113.50"}, wantIDs: nil, golden: "diagnose-pod-network-only.json",
		},
		{
			name: "missing host route", fixture: "pod-network-only.yaml", snapshot: "worker-3-pod-network.json",
			mutate: func(s *host.Snapshot) { s.Routes = s.Routes[1:] }, // drop the default route
			args:   []string{"vm/web-04", "--destination", "203.0.113.50"}, wantIDs: []string{"HOST_ROUTE_MISSING"},
			golden: "diagnose-missing-route.json",
		},
		{
			name: "multus linux bridge attachment healthy", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			args: []string{"vm/web-01", "-o", "text"}, wantIDs: nil, golden: "diagnose-linux-bridge.txt",
		},
		{
			name: "linux bridge missing on host", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			mutate: func(s *host.Snapshot) {
				var keep []host.Interface
				for _, i := range s.Interfaces {
					if i.Name != "br1" {
						keep = append(keep, i)
					}
				}
				s.Interfaces = keep
			},
			args: []string{"vm/web-01"}, wantIDs: []string{"HOST_INTERFACE_MISSING"}, golden: "diagnose-bridge-missing.json",
		},
		{
			name: "linux bridge uplink does not carry VLAN", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			mutate: func(s *host.Snapshot) { s.BridgeVLANs[0].VLANs[1] = host.PortVLANs{Min: 200, Max: 210} },
			args:   []string{"vm/web-01"}, wantIDs: []string{"VLAN_MISMATCH"}, golden: "diagnose-bridge-vlan-mismatch.json",
		},
		{
			name: "linux bridge without VLAN filtering", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			mutate: func(s *host.Snapshot) { s.FindInterface("br1").Bridge.VLANFiltering = false },
			args:   []string{"vm/web-01"}, wantIDs: []string{"VLAN_NOT_ENFORCED"}, golden: "diagnose-vlan-not-enforced.json",
		},
		{
			name: "linux bond member detached with different LACP partner", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			mutate: func(s *host.Snapshot) {
				m := s.FindInterface("eno2").BondMember
				m.AggregatorID, m.PartnerMAC = 2, "02:bb:00:00:00:02"
			},
			args: []string{"vm/web-01"}, wantIDs: []string{"BOND_MEMBER_DETACHED", "LACP_PARTNER_MISMATCH"}, golden: "diagnose-linux-bond.json",
		},
		{
			name: "linux bond text output", fixture: "bridge-vlan-running.yaml", snapshot: "worker-1-linux-bridge.json",
			mutate: func(s *host.Snapshot) {
				m := s.FindInterface("eno2").BondMember
				m.AggregatorID, m.PartnerMAC = 2, "02:bb:00:00:00:02"
			},
			args: []string{"vm/web-01", "-o", "text"}, golden: "diagnose-linux-bond.txt",
		},
		{
			name: "ovs vlan attachment healthy", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			args: []string{"vm/db-03", "--destination", "203.0.113.9", "-o", "text"}, wantIDs: nil, golden: "diagnose-ovs-healthy.txt",
		},
		{
			name: "ovs port missing", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) {
				s.OVS.Ports = s.OVS.Ports[:2]
				s.OVS.Bridges[0].Ports = s.OVS.Bridges[0].Ports[:2]
			},
			args: []string{"vm/db-03"}, wantIDs: []string{"OVS_PORT_MISSING"}, golden: "diagnose-ovs-port-missing.json",
		},
		{
			name: "ovs port VLAN tag mismatch", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) { ovsPort(s, "veth3e9a1c7b").Tag = intp(201) },
			args:   []string{"vm/db-03"}, wantIDs: []string{"VLAN_MISMATCH"}, golden: "diagnose-ovs-vlan-mismatch.json",
		},
		{
			name: "ovs uplink MTU below NAD MTU", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) { s.OVS.FindInterface("ens1f1").MTU = intp(1500) },
			args:   []string{"vm/db-03"}, wantIDs: []string{"MTU_MISMATCH"}, golden: "diagnose-ovs-mtu-mismatch.json",
		},
		{
			name: "ovs bond member detached", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) {
				m := &s.OVS.Bonds[0].Members[1]
				m.Enabled, m.LACPState, m.PartnerSysID = false, "defaulted detached", "00:00:00:00:00:00"
			},
			args: []string{"vm/db-03"}, wantIDs: []string{"BOND_MEMBER_DETACHED"}, golden: "diagnose-ovs-bond-detached.json",
		},
		{
			name: "ovs LACP partner mismatch", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) { s.OVS.Bonds[0].Members[1].PartnerSysID = "02:bb:00:00:00:02" },
			args:   []string{"vm/db-03"}, wantIDs: []string{"LACP_PARTNER_MISMATCH"}, golden: "diagnose-ovs-lacp-mismatch.json",
		},
		{
			name: "host collector lacked OVS privileges", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) {
				s.OVS = nil
				setSource(s, host.SourceOVSDB, host.StatusFailed, "exit status 1: database connection failed (Permission denied) (this source requires elevated privileges; see the host collector section of the README)")
				setSource(s, host.SourceOVSBonds, host.StatusSkipped, "")
				setSource(s, host.SourceOVSLACP, host.StatusSkipped, "")
				setSource(s, host.SourceOVSFlows, host.StatusSkipped, "")
			},
			args: []string{"vm/db-03"}, wantIDs: []string{"HOST_COLLECTOR_UNAVAILABLE", "TOPOLOGY_INCOMPLETE"}, golden: "diagnose-host-permissions.json",
		},
		{
			name: "snapshot from another node", fixture: "ovs-vlan-running.yaml", snapshot: "worker-2-ovs.json",
			mutate: func(s *host.Snapshot) { s.NodeName = "worker-9" },
			args:   []string{"vm/db-03"}, wantIDs: []string{"HOST_SNAPSHOT_NODE_MISMATCH", "TOPOLOGY_INCOMPLETE"}, golden: "diagnose-node-mismatch.json",
		},
		{
			name: "missing NAD without host data", fixture: "nad-missing-scheduling.yaml",
			args:    []string{"vm/app-03", "--destination", "192.0.2.50"},
			wantIDs: []string{"LAUNCHER_POD_MISSING", "NAD_NOT_FOUND", "TOPOLOGY_INCOMPLETE", "VMI_NOT_RUNNING"},
			golden:  "diagnose-missing-nad.json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients := fakecluster.New(t, []string{fixture(tc.fixture)})
			args := append([]string{"diagnose"}, tc.args...)
			args = append(args, "-n", "demo")
			if tc.snapshot != "" {
				args = append(args, "--host-snapshot", snapshotFile(t, tc.snapshot, tc.mutate))
			}
			isJSON := !strings.HasSuffix(tc.golden, ".txt")
			if isJSON {
				args = append(args, "-o", "json")
			}
			out, err := run(t, clients, args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if isJSON {
				assertFindingIDs(t, out, tc.wantIDs)
			}
			assertGolden(t, tc.golden, out)
		})
	}
}

// assertFindingIDs checks the distinct finding IDs in a JSON report.
func assertFindingIDs(t *testing.T, out string, want []string) {
	t.Helper()
	var r struct {
		Findings []struct {
			ID string `json:"id"`
		} `json:"findings"`
		Summary struct {
			Connectivity string `json:"connectivity"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if r.Summary.Connectivity != "NotTested" {
		t.Errorf("connectivity = %q, must be NotTested", r.Summary.Connectivity)
	}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		seen[f.ID] = true
	}
	got := make([]string, 0, len(seen))
	for id := range seen {
		got = append(got, id)
	}
	sort.Strings(got)
	wantSorted := append([]string{}, want...)
	sort.Strings(wantSorted)
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Errorf("finding IDs = %v, want %v", got, wantSorted)
	}
}

func TestTopologyScenarios(t *testing.T) {
	tests := []struct {
		name, fixture, snapshot, golden string
		args                            []string
	}{
		{"ovs topology with host snapshot", "ovs-vlan-running.yaml", "worker-2-ovs.json", "topology-ovs.txt", []string{"vm/db-03"}},
		{"ovs topology json", "ovs-vlan-running.yaml", "worker-2-ovs.json", "topology-ovs.json", []string{"vm/db-03", "-o", "json"}},
		{"linux bridge topology", "bridge-vlan-running.yaml", "worker-1-linux-bridge.json", "topology-linux-bridge.txt", []string{"vm/web-01"}},
		{"topology without host snapshot", "attachment-missing.yaml", "", "topology-no-host.txt", []string{"vmi/edge-05"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients := fakecluster.New(t, []string{fixture(tc.fixture)})
			args := append([]string{"topology"}, tc.args...)
			args = append(args, "-n", "demo")
			if tc.snapshot != "" {
				args = append(args, "--host-snapshot", snapshotFile(t, tc.snapshot, nil))
			}
			out, err := run(t, clients, args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertGolden(t, tc.golden, out)
		})
	}
}

func TestDiagnoseExpectations(t *testing.T) {
	exp := filepath.Join(t.TempDir(), "expect.yaml")
	if err := os.WriteFile(exp, []byte(`
networks:
  data:
    vlan: 300
    mtu: 9000
bonds:
  bond1:
    partnerSystemID: "02:cc:00:00:00:03"
    members: [ens1f0, ens1f1, ens1f2]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	clients := fakecluster.New(t, []string{fixture("ovs-vlan-running.yaml")})
	out, err := run(t, clients, "diagnose", "vm/db-03", "-n", "demo", "-o", "json",
		"--host-snapshot", snapshotFile(t, "worker-2-ovs.json", nil), "--expectations", exp)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingIDs(t, out, []string{"BOND_MEMBER_DETACHED", "LACP_PARTNER_MISMATCH", "VLAN_MISMATCH"})
	for _, want := range []string{"VLAN 300 (expectations file)", "partner system 02:cc:00:00:00:03 (expectations file)", "ens1f2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q", want)
		}
	}
}

func TestDiagnoseInputErrors(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schemaVersion":"other","nodeName":"x","sources":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"bad destination":      {"diagnose", "vm/web-04", "-n", "demo", "--destination", "not-an-ip"},
		"missing snapshot":     {"diagnose", "vm/web-04", "-n", "demo", "--host-snapshot", filepath.Join(t.TempDir(), "none.json")},
		"wrong schema":         {"diagnose", "vm/web-04", "-n", "demo", "--host-snapshot", bad},
		"missing expectations": {"diagnose", "vm/web-04", "-n", "demo", "--expectations", filepath.Join(t.TempDir(), "none.yaml")},
		"bad topology output":  {"topology", "vm/web-04", "-n", "demo", "-o", "yaml"},
	} {
		if _, err := run(t, clients, args...); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
