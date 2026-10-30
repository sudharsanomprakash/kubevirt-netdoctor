package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseLinks(t *testing.T) {
	links, err := ParseLinks(read(t, "ip-link.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{Interfaces: links}

	bond := snap.FindInterface("bond0")
	if bond == nil || bond.Kind != "bond" || bond.Bond == nil || bond.Bond.Mode != "802.3ad" || bond.Bond.ActiveAggregator != 1 {
		t.Fatalf("bond0: %+v", bond)
	}
	eno2 := snap.FindInterface("eno2")
	if eno2.BondMember == nil || eno2.BondMember.MIIStatus != "DOWN" || eno2.BondMember.AggregatorID != 2 || eno2.Master != "bond0" {
		t.Errorf("eno2: %+v %+v", eno2, eno2.BondMember)
	}
	if !eno2.IsDown() {
		t.Error("eno2 has no UP flag and must be down")
	}
	vlan := snap.FindInterface("bond0.100")
	if vlan.VLAN == nil || vlan.VLAN.ID != 100 || vlan.Parent != "bond0" || vlan.Master != "br1" {
		t.Errorf("bond0.100: %+v", vlan)
	}
	if br := snap.FindInterface("br1"); br.Bridge == nil || !br.Bridge.VLANFiltering || br.MTU != 1500 {
		t.Errorf("br1: %+v", br)
	}
	if veth := snap.FindInterface("veth5c6f2b9e"); veth.PeerNetnsID == nil || *veth.PeerNetnsID != 0 {
		t.Errorf("veth peer netns: %+v", veth)
	}
	if lo := snap.FindInterface("lo"); lo.IsDown() {
		t.Error("UNKNOWN operstate with UP flag must not be considered down")
	}
	if got := len(snap.Members("bond0")); got != 2 {
		t.Errorf("bond0 members = %d", got)
	}
	if _, err := ParseLinks([]byte("not json")); err == nil {
		t.Error("expected error")
	}
}

func TestParseAddressesAndRoutes(t *testing.T) {
	addrs, err := ParseAddresses(read(t, "ip-addr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(addrs["br1"], []string{"192.0.2.11/24", "2001:db8::11/64"}) {
		t.Errorf("br1 addrs = %v", addrs["br1"])
	}

	r4, err := ParseRoutes(read(t, "ip-route4.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Table: "main", Dst: "0.0.0.0/0", Gateway: "192.0.2.1", Dev: "br1", Protocol: "static", Metric: 100},
		{Table: "main", Dst: "192.0.2.0/24", Dev: "br1", Protocol: "kernel"},
		{Table: "main", Dst: "198.51.100.7/32", Gateway: "192.0.2.254", Dev: "br1"},
		{Table: "local", Dst: "192.0.2.11/32", Dev: "br1", Protocol: "kernel", Type: "local"},
	}
	if !reflect.DeepEqual(r4, want) {
		t.Errorf("routes:\n got %+v\nwant %+v", r4, want)
	}
	r6, err := ParseRoutes(read(t, "ip-route6.json"), true)
	if err != nil || len(r6) != 1 || r6[0].Dst != "2001:db8::/64" {
		t.Errorf("ipv6 routes: %+v %v", r6, err)
	}
	if def, _ := ParseRoutes([]byte(`[{"dst":"default","dev":"eth0"}]`), true); def[0].Dst != "::/0" {
		t.Errorf("ipv6 default = %s", def[0].Dst)
	}

	// Unnamed protocols and table IDs may be printed as numbers; table 254 is main.
	numeric, err := ParseRoutes([]byte(`[{"type":"blackhole","dst":"192.0.2.0/24","protocol":80},
		{"dst":"198.51.100.0/24","dev":"eth0","table":254},{"dst":"203.0.113.0/24","dev":"eth0","table":100}]`), false)
	if err != nil {
		t.Fatalf("numeric fields: %v", err)
	}
	if numeric[0].Protocol != "80" || numeric[0].Type != "blackhole" || numeric[1].Table != "main" || numeric[2].Table != "100" {
		t.Errorf("numeric fields: %+v", numeric)
	}
	if _, err := ParseRoutes([]byte(`[{"dst":"192.0.2.0/24","protocol":{"x":1}}]`), false); err == nil {
		t.Error("an object where a string or number is expected must fail")
	}
}

func TestParseBridgeVLANs(t *testing.T) {
	vlans, err := ParseBridgeVLANs(read(t, "bridge-vlan.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{BridgeVLANs: vlans}
	got, ok := snap.PortVLANs("bond0.100")
	want := []PortVLANs{{Min: 1, Max: 1, PVID: true, Untagged: true}, {Min: 100, Max: 100}, {Min: 200, Max: 210}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("bond0.100 vlans = %+v", got)
	}

	legacy, err := ParseBridgeVLANs(read(t, "bridge-vlan-legacy.json"))
	if err != nil || len(legacy) != 1 || legacy[0].Port != "eth1" || len(legacy[0].VLANs) != 2 {
		t.Errorf("legacy form: %+v %v", legacy, err)
	}
}

func TestParseNamespaces(t *testing.T) {
	if ns, err := ParseNamespaces(nil); err != nil || ns != nil {
		t.Errorf("empty output: %v %v", ns, err)
	}
	ns, err := ParseNamespaces([]byte(`[{"name":"cni-0000","id":0},{"name":"cni-0001"}]`))
	if err != nil || !reflect.DeepEqual(ns, []string{"cni-0000", "cni-0001"}) {
		t.Errorf("namespaces: %v %v", ns, err)
	}
}

func TestParseProcBonding(t *testing.T) {
	b := ParseProcBonding(read(t, "proc-bonding-bond0.txt"))
	if b.ActiveAggregator != "1" || !strings.Contains(b.Mode, "802.3ad") {
		t.Errorf("bond: %+v", b)
	}
	if m := b.Members["eno1"]; m.PartnerMAC != "02:aa:00:00:00:01" || m.AggregatorID != "1" || m.MIIStatus != "up" {
		t.Errorf("eno1: %+v", m)
	}
	// The actor PDU's system mac must not be mistaken for the partner's.
	if m := b.Members["eno2"]; m.PartnerMAC != "02:bb:00:00:00:02" || m.AggregatorID != "2" {
		t.Errorf("eno2: %+v", m)
	}
}

func TestParseOVSDB(t *testing.T) {
	ovs, err := ParseOVSDB(read(t, "ovs-bridge.json"), read(t, "ovs-port.json"), read(t, "ovs-interface.json"))
	if err != nil {
		t.Fatal(err)
	}
	br := ovs.FindBridge("br-data")
	if br == nil || !reflect.DeepEqual(br.Ports, []string{"bond1", "br-data", "veth8a1b2c3d"}) {
		t.Fatalf("br-data: %+v", br)
	}
	if sec := ovs.FindBridge("br-secure"); sec.FailMode != "secure" {
		t.Errorf("br-secure fail mode = %q", sec.FailMode)
	}
	bond := ovs.FindPort("bond1")
	if bond.Bridge != "br-data" || !reflect.DeepEqual(bond.Interfaces, []string{"ens1f0", "ens1f1"}) ||
		!reflect.DeepEqual(bond.Trunks, []int{200, 300}) || bond.Tag != nil || bond.BondMode != "balance-tcp" || bond.LACP != "active" {
		t.Errorf("bond1: %+v", bond)
	}
	pod := ovs.FindPort("veth8a1b2c3d")
	if pod.Tag == nil || *pod.Tag != 200 || pod.VLANMode != "access" ||
		pod.ExternalIDs[ExternalIDOwner] != OVSCNIOwner || pod.ExternalIDs[ExternalIDContIface] != "eth0" {
		t.Errorf("pod port: %+v", pod)
	}
	if bond.ExternalIDs != nil {
		t.Errorf("empty map must decode to nil, got %v", bond.ExternalIDs)
	}
	i3 := ovs.FindInterface("ens1f1")
	if i3.LinkState != "down" || i3.MTU == nil || *i3.MTU != 9000 || i3.OFPort != 2 {
		t.Errorf("ens1f1: %+v", i3)
	}
	if e := ovs.FindInterface("br-secure").Error; e != "could not open network device" {
		t.Errorf("error column = %q", e)
	}
	if _, err := ParseOVSDB([]byte(`{"data":[["x"]],"headings":["a","b"]}`), read(t, "ovs-port.json"), read(t, "ovs-interface.json")); err == nil {
		t.Error("expected column count error")
	}
}

func TestParseOVSBondAndLACP(t *testing.T) {
	bonds := ParseOVSBondShow(read(t, "ovs-bond-show.txt"))
	if len(bonds) != 1 || bonds[0].Name != "bond1" || bonds[0].Mode != "balance-tcp" || bonds[0].LACPStatus != "negotiated" {
		t.Fatalf("bonds: %+v", bonds)
	}
	bonds = MergeOVSLACPShow(bonds, read(t, "ovs-lacp-show.txt"))
	want := []OVSBondMember{
		{Name: "ens1f0", Enabled: true, LACPState: "current attached", PartnerSysID: "02:aa:00:00:00:01"},
		{Name: "ens1f1", Enabled: false, LACPState: "defaulted detached", PartnerSysID: "00:00:00:00:00:00"},
	}
	if !reflect.DeepEqual(bonds[0].Members, want) {
		t.Errorf("members:\n got %+v\nwant %+v", bonds[0].Members, want)
	}
}

func TestParseOVSFlows(t *testing.T) {
	if s := ParseOVSFlows(read(t, "ovs-flows-br-data.txt")); s.Count != 1 || !s.HasNormal {
		t.Errorf("br-data flows: %+v", s)
	}
	if s := ParseOVSFlows(read(t, "ovs-flows-br-secure.txt")); s.Count != 0 || s.HasNormal {
		t.Errorf("br-secure flows: %+v", s)
	}
}

// fakeRunner maps command lines to fixture files or errors.
type fakeRunner struct {
	files          map[string]string
	errs           map[string]error
	missing        map[string]bool
	executed       []string
	blockUntilDone bool
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.missing[name] {
		return "", errors.New("executable file not found in $PATH")
	}
	return "/usr/sbin/" + name, nil
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.executed = append(f.executed, line)
	if f.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err, ok := f.errs[line]; ok {
		return nil, err
	}
	file, ok := f.files[line]
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join("testdata", file))
	return data, err
}

func allCommands() map[string]string {
	return map[string]string{
		strings.Join(cmdLinks, " "):       "ip-link.json",
		strings.Join(cmdAddrs, " "):       "ip-addr.json",
		strings.Join(cmdRoutes4, " "):     "ip-route4.json",
		strings.Join(cmdRoutes6, " "):     "ip-route6.json",
		strings.Join(cmdBridgeVLANs, " "): "bridge-vlan.json",
		strings.Join(cmdOVSBridges, " "):  "ovs-bridge.json",
		strings.Join(cmdOVSPorts, " "):    "ovs-port.json",
		strings.Join(cmdOVSIfaces, " "):   "ovs-interface.json",
		strings.Join(cmdOVSBondShow, " "): "ovs-bond-show.txt",
		strings.Join(cmdOVSLACPShow, " "): "ovs-lacp-show.txt",
		"ovs-ofctl dump-flows br-data":    "ovs-flows-br-data.txt",
		"ovs-ofctl dump-flows br-secure":  "ovs-flows-br-secure.txt",
	}
}

func procFS(t *testing.T) fstest.MapFS {
	return fstest.MapFS{"proc/net/bonding/bond0": {Data: read(t, "proc-bonding-bond0.txt")}}
}

var fixedNow = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func statuses(s *Snapshot) map[string]string {
	out := map[string]string{}
	for _, src := range s.Sources {
		out[src.Name] = src.Status
	}
	return out
}

func TestCollectFull(t *testing.T) {
	r := &fakeRunner{files: allCommands()}
	snap, err := Collect(context.Background(), r, procFS(t), CollectOptions{NodeName: "worker-1", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range statuses(snap) {
		if st != StatusOK {
			t.Errorf("source %s = %s", name, st)
		}
	}
	if snap.NodeName != "worker-1" || snap.CollectedAt != "2026-01-01T00:00:00Z" || snap.SchemaVersion != SchemaVersion {
		t.Errorf("metadata: %+v", snap)
	}
	if br := snap.FindInterface("br1"); len(br.Addresses) != 2 {
		t.Errorf("addresses not merged: %+v", br)
	}
	if eno1 := snap.FindInterface("eno1"); eno1.BondMember.PartnerMAC != "02:aa:00:00:00:01" {
		t.Errorf("partner MAC not merged from /proc: %+v", eno1.BondMember)
	}
	if snap.OVS == nil || snap.OVS.FindBridge("br-data").Flows == nil || snap.OVS.FindBond("bond1") == nil {
		t.Fatalf("OVS: %+v", snap.OVS)
	}
	if f := snap.OVS.FindBridge("br-secure").Flows; f == nil || f.Count != 0 {
		t.Errorf("br-secure flows: %+v", f)
	}
	// Only allowlisted commands may run.
	allowed := allCommands()
	for _, line := range r.executed {
		if _, ok := allowed[line]; !ok && line != strings.Join(cmdNetns, " ") {
			t.Errorf("unexpected command executed: %q", line)
		}
	}
}

func TestCollectWithoutOVSAndWithoutPrivileges(t *testing.T) {
	denied := errors.New("exit status 1: ovs-vsctl: unix:/var/run/openvswitch/db.sock: database connection failed (Permission denied)")
	r := &fakeRunner{
		files: allCommands(),
		errs:  map[string]error{strings.Join(cmdOVSBridges, " "): denied},
	}
	snap, err := Collect(context.Background(), r, fstest.MapFS{}, CollectOptions{NodeName: "worker-1", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	st := statuses(snap)
	if st[SourceOVSDB] != StatusFailed || snap.OVS != nil {
		t.Errorf("ovsdb = %s, ovs = %+v", st[SourceOVSDB], snap.OVS)
	}
	src, _ := snap.Source(SourceOVSDB)
	if !strings.Contains(src.Message, "elevated privileges") {
		t.Errorf("permission hint missing: %q", src.Message)
	}
	if st[SourceBonding] != StatusFailed {
		t.Errorf("unreadable /proc/net/bonding must be Failed, got %s", st[SourceBonding])
	}
	if st[SourceLinks] != StatusOK {
		t.Errorf("Linux sources must still be collected: %v", st)
	}

	r = &fakeRunner{files: allCommands(), missing: map[string]bool{"ovs-vsctl": true}}
	snap, _ = Collect(context.Background(), r, procFS(t), CollectOptions{Now: fixedNow})
	if statuses(snap)[SourceOVSDB] != StatusNotInstalled {
		t.Errorf("missing ovs-vsctl must be NotInstalled: %v", statuses(snap))
	}

	snap, _ = Collect(context.Background(), &fakeRunner{files: allCommands()}, procFS(t), CollectOptions{SkipOVS: true, Now: fixedNow})
	if statuses(snap)[SourceOVSDB] != StatusSkipped || snap.OVS != nil {
		t.Errorf("--skip-ovs: %v", statuses(snap))
	}
}

func TestCollectTimeoutAndCancellation(t *testing.T) {
	r := &fakeRunner{blockUntilDone: true}
	snap, err := Collect(context.Background(), r, fstest.MapFS{}, CollectOptions{SkipOVS: true, CommandTimeout: 10 * time.Millisecond, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := snap.Source(SourceLinks)
	if src.Status != StatusFailed || !strings.Contains(src.Message, "timed out") {
		t.Errorf("links: %+v", src)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, &fakeRunner{blockUntilDone: true}, fstest.MapFS{}, CollectOptions{Now: fixedNow}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: err = %v", err)
	}
}

func TestBridgeNameValidation(t *testing.T) {
	for name, want := range map[string]bool{
		"br-data": true, "br_ex": true, "br0.100": true,
		"-oProtocols": false, "": false, "a b": false, "br;rm": false, strings.Repeat("a", 65): false,
	} {
		if got := validBridgeName(name); got != want {
			t.Errorf("validBridgeName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNamesAgree(t *testing.T) {
	for _, tc := range []struct {
		node, hostname string
		want           bool
	}{
		{"worker-1", "worker-1", true},
		{"worker-1.example.internal", "worker-1", true},
		{"Worker-1.example.internal", "worker-1.lab", true},
		{"worker-2.example.internal", "worker-1", false},
		{"worker-1", "", true},
	} {
		if got := NamesAgree(tc.node, tc.hostname); got != tc.want {
			t.Errorf("NamesAgree(%q, %q) = %v", tc.node, tc.hostname, got)
		}
	}
}
