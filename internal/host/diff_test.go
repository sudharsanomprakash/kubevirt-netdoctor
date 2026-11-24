package host

import "testing"

func TestDiffSnapshots(t *testing.T) {
	tag100, tag200 := 100, 200
	before := &Snapshot{
		NodeName:    "worker-1",
		CollectedAt: "2026-01-01T00:00:00Z",
		Interfaces: []Interface{
			{Name: "eth0", MTU: 1500, OperState: "UP", AdminUp: true, Addresses: []string{"192.0.2.11/24"}},
		},
		Routes: []Route{{Table: "main", Dst: "0.0.0.0/0", Gateway: "192.0.2.1", Dev: "eth0"}},
		BridgeVLANs: []BridgePortVLAN{
			{Port: "br1", VLANs: []PortVLANs{{Min: 1, Max: 1, PVID: true, Untagged: true}, {Min: 100, Max: 100}}},
		},
		OVS: &OVS{
			Bridges: []OVSBridge{{Name: "br-data", FailMode: "secure"}},
			Ports:   []OVSPort{{Name: "veth1", Bridge: "br-data", Tag: &tag100}},
		},
	}
	after := &Snapshot{
		NodeName:    "worker-1",
		CollectedAt: "2026-01-01T01:00:00Z",
		Interfaces: []Interface{
			{Name: "eth0", MTU: 9000, OperState: "UP", AdminUp: true, Addresses: []string{"192.0.2.12/24"}},
			{Name: "eth1", MTU: 1500, OperState: "UP", AdminUp: true},
		},
		Routes: []Route{{Table: "main", Dst: "0.0.0.0/0", Gateway: "192.0.2.254", Dev: "eth0"}},
		BridgeVLANs: []BridgePortVLAN{
			{Port: "br1", VLANs: []PortVLANs{{Min: 1, Max: 1, PVID: true, Untagged: true}, {Min: 200, Max: 200}}},
		},
		OVS: &OVS{
			Bridges: []OVSBridge{{Name: "br-data", FailMode: "standalone"}},
			Ports:   []OVSPort{{Name: "veth1", Bridge: "br-data", Tag: &tag200}},
		},
	}

	diff := DiffSnapshots(before, after)

	find := func(object, name, field string) (Change, bool) {
		for _, c := range diff.Changes {
			if c.Object == object && c.Name == name && c.Field == field {
				return c, true
			}
		}
		return Change{}, false
	}
	for _, want := range []struct{ object, name, field, before, after string }{
		{"interface", "eth0", "mtu", "1500", "9000"},
		{"interface", "eth0", "addresses", "192.0.2.11/24", "192.0.2.12/24"},
		{"interface", "eth1", "present", "no", "yes"},
		{"route", "main 0.0.0.0/0", "gateway", "192.0.2.1", "192.0.2.254"},
		{"bridge-vlan", "br1", "vlans", "1-1/pvid/untagged,100-100", "1-1/pvid/untagged,200-200"},
		{"ovs-bridge", "br-data", "failMode", "secure", "standalone"},
		{"ovs-port", "br-data/veth1", "tag", "100", "200"},
	} {
		got, ok := find(want.object, want.name, want.field)
		if !ok {
			t.Errorf("missing change %s %s %s", want.object, want.name, want.field)
			continue
		}
		if got.Before != want.before || got.After != want.after {
			t.Errorf("%s %s %s = %q -> %q, want %q -> %q", want.object, want.name, want.field, got.Before, got.After, want.before, want.after)
		}
	}
	if len(diff.Changes) != 7 {
		t.Errorf("got %d changes, want 7: %+v", len(diff.Changes), diff.Changes)
	}
}

func TestDiffSnapshotsIdenticalAndRemoved(t *testing.T) {
	snap := &Snapshot{
		NodeName:   "worker-1",
		Interfaces: []Interface{{Name: "eth0", MTU: 1500}},
		Routes:     []Route{{Table: "main", Dst: "192.0.2.0/24", Dev: "eth0"}},
	}
	if diff := DiffSnapshots(snap, snap); len(diff.Changes) != 0 {
		t.Errorf("identical snapshots produced %+v", diff.Changes)
	}
	empty := &Snapshot{NodeName: "worker-1"}
	diff := DiffSnapshots(snap, empty)
	if len(diff.Changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(diff.Changes), diff.Changes)
	}
	for _, c := range diff.Changes {
		if c.Field != "present" || c.Before != "yes" || c.After != "no" {
			t.Errorf("removed object change = %+v", c)
		}
	}
}
