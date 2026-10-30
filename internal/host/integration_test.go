package host

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestIntegrationCollect runs the real collector against real iproute2
// output. It is skipped unless NETDOCTOR_INTEGRATION=1; run it through
// hack/integration-netns.sh, which builds the expected topology in a
// disposable network namespace.
func TestIntegrationCollect(t *testing.T) {
	if os.Getenv("NETDOCTOR_INTEGRATION") != "1" {
		t.Skip("set NETDOCTOR_INTEGRATION=1 (see hack/integration-netns.sh)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := Collect(ctx, ExecRunner{}, os.DirFS("/"), CollectOptions{NodeName: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{SourceLinks, SourceAddresses, SourceRoutes, SourceBridgeVLANs, SourceNamespaces} {
		if src, _ := snap.Source(name); src.Status != StatusOK {
			t.Errorf("source %s: %+v", name, src)
		}
	}

	vlanSupported := os.Getenv("NETDOCTOR_INTEGRATION_VLAN") != "0"
	br := snap.FindInterface("br1")
	if br == nil || br.Kind != "bridge" || br.Bridge == nil || br.Bridge.VLANFiltering != vlanSupported || br.MTU != 1400 || !br.AdminUp {
		t.Fatalf("br1: %+v (bridge %+v)", br, br.Bridge)
	}
	if len(br.Addresses) == 0 || br.Addresses[0] != "192.0.2.10/24" {
		t.Errorf("br1 addresses: %v", br.Addresses)
	}
	veth := snap.FindInterface("vethhost")
	if veth == nil || veth.Kind != "veth" || veth.Master != "br1" || veth.PeerNetnsID == nil {
		t.Fatalf("vethhost: %+v", veth)
	}

	if vlanSupported {
		vlans, ok := snap.PortVLANs("vethhost")
		if !ok {
			t.Fatalf("no bridge VLANs for vethhost: %+v", snap.BridgeVLANs)
		}
		var access, trunk bool
		for _, v := range vlans {
			if v.Min == 100 && v.Max == 100 && v.PVID && v.Untagged {
				access = true
			}
			if v.Min == 200 && v.Max == 210 && !v.PVID {
				trunk = true
			}
		}
		if !access || !trunk {
			t.Errorf("vethhost VLANs: %+v", vlans)
		}
	} else {
		t.Log("bridge VLAN filtering unsupported by this kernel: VLAN table checks skipped")
	}

	var def, blackhole bool
	for _, r := range snap.Routes {
		if r.Table == "main" && r.Dst == "0.0.0.0/0" && r.Gateway == "192.0.2.1" && r.Dev == "br1" && r.Type == "" {
			def = true
		}
		if r.Table == "main" && r.Dst == "198.51.100.0/24" && r.Type == "blackhole" {
			blackhole = true
		}
	}
	if !def || !blackhole {
		t.Errorf("routes: %+v", snap.Routes)
	}
	t.Logf("sources: %+v", snap.Sources)
}
