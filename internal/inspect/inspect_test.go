package inspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"

	fakediscovery "k8s.io/client-go/discovery/fake"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

func vm(name string) topology.Target {
	return topology.Target{Kind: topology.KindVM, Namespace: "demo", Name: name}
}

func vmi(name string) topology.Target {
	return topology.Target{Kind: topology.KindVMI, Namespace: "demo", Name: name}
}

func TestInspectRunningVM(t *testing.T) {
	c := fakecluster.New(t, []string{fixture("bridge-vlan-running.yaml")})
	topo, err := Inspect(context.Background(), c, vm("web-01"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.Instance.Lookup != kube.StateFound || topo.Instance.NetworkSpecSource != topology.SpecFromVMI {
		t.Errorf("instance: %+v", topo.Instance)
	}
	if topo.Launcher.Selected == nil || topo.Launcher.Selected.Name != "virt-launcher-web-01-x7k2p" {
		t.Errorf("launcher: %+v", topo.Launcher)
	}
	if topo.NodeName != "worker-1" || len(topo.Networks) != 2 || len(topo.AccessIssues) != 0 {
		t.Errorf("topology: node=%s networks=%d issues=%v", topo.NodeName, len(topo.Networks), topo.AccessIssues)
	}
}

func TestInspectStoppedVMUsesTemplate(t *testing.T) {
	c := fakecluster.New(t, []string{fixture("stopped-invalid-nad.yaml")})
	topo, err := Inspect(context.Background(), c, vm("batch-04"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.Instance.Lookup != kube.StateNotFound || topo.Instance.NetworkSpecSource != topology.SpecFromVMTemplate {
		t.Errorf("instance: %+v", topo.Instance)
	}
	if topo.Launcher.Lookup != kube.StateNotChecked {
		t.Errorf("launcher should not be looked up without a VMI: %+v", topo.Launcher)
	}
	if len(topo.Networks) != 3 {
		t.Fatalf("networks from template: %d", len(topo.Networks))
	}
	// The cross-namespace reference resolves to infra/legacy-net.
	if n := topo.Networks[2]; n.NAD == nil || n.NAD.Ref.String() != "infra/legacy-net" || n.NAD.Lookup != kube.StateFound {
		t.Errorf("legacy nad: %+v", n.NAD)
	}
}

func TestInspectVMIGetForbiddenFallsBackToTemplate(t *testing.T) {
	c := fakecluster.New(t, []string{fixture("bridge-vlan-running.yaml")}, fakecluster.Forbid("get", "virtualmachineinstances"))
	topo, err := Inspect(context.Background(), c, vm("web-01"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.Instance.Lookup != kube.StateForbidden || topo.Instance.NetworkSpecSource != topology.SpecFromVMTemplate {
		t.Errorf("instance: %+v", topo.Instance)
	}
	if len(topo.AccessIssues) != 1 || topo.AccessIssues[0].Resource != "virtualmachineinstances.kubevirt.io" {
		t.Errorf("access issues: %+v", topo.AccessIssues)
	}
	if len(topo.Networks) != 2 || topo.Networks[1].NAD.Lookup != kube.StateFound {
		t.Errorf("template networks should still be checked: %+v", topo.Networks)
	}
}

func TestInspectOwnerVMForbidden(t *testing.T) {
	c := fakecluster.New(t, []string{fixture("bridge-vlan-running.yaml")}, fakecluster.Forbid("get", "virtualmachines"))
	topo, err := Inspect(context.Background(), c, vmi("web-01"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.VirtualMachine == nil || topo.VirtualMachine.Lookup != kube.StateForbidden {
		t.Errorf("owner VM: %+v", topo.VirtualMachine)
	}
	if topo.Launcher.Selected == nil {
		t.Error("launcher discovery must continue when the owner VM is unreadable")
	}
}

func TestInspectStandaloneVMIHasNoVM(t *testing.T) {
	c := fakecluster.New(t, []string{fixture("ovs-no-guest-agent.yaml")})
	topo, err := Inspect(context.Background(), c, vmi("db-02"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.VirtualMachine != nil {
		t.Errorf("standalone VMI must not report a VM: %+v", topo.VirtualMachine)
	}
	if topo.Instance.GuestAgent != topology.AgentNotConnected {
		t.Errorf("guest agent: %s", topo.Instance.GuestAgent)
	}
}

func TestInspectKubeVirtNotInstalled(t *testing.T) {
	c := fakecluster.New(t, nil)
	c.Core.Discovery().(*fakediscovery.FakeDiscovery).Resources = nil
	_, err := Inspect(context.Background(), c, vm("web-01"))
	var te *TargetError
	if !errors.As(err, &te) || te.State != kube.StateUnavailable {
		t.Fatalf("expected unavailable TargetError, got %v", err)
	}
}

func TestInspectUnsupportedKind(t *testing.T) {
	c := fakecluster.New(t, nil)
	if _, err := Inspect(context.Background(), c, topology.Target{Kind: "Pod", Namespace: "demo", Name: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}

// Fixtures must stay synthetic: guard against accidentally committing
// real-looking public addresses by restricting IPv4 literals in fixtures to
// documentation and private ranges.
func TestFixturesUseSyntheticAddresses(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		fixture("*.yaml"),
		filepath.Join("..", "..", "testdata", "hosts", "*.json"),
		filepath.Join("..", "host", "testdata", "*"),
		filepath.Join("..", "..", "examples", "*.yaml"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			t.Fatalf("no files match %s: %v", pattern, err)
		}
		files = append(files, matches...)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, ip := range ipv4Pattern.FindAllString(string(data), -1) {
			if !syntheticIPv4(ip) {
				t.Errorf("%s: %s is not in a documentation or private range", filepath.Base(f), ip)
			}
		}
	}
}

// The example manifests shipped in examples/ must be internally consistent:
// every referenced NAD exists and its CNI config parses without warnings.
func TestExamplesAreConsistent(t *testing.T) {
	for file, vmName := range map[string]string{
		"linux-bridge-vlan.yaml": "web-01",
		"ovs-vlan.yaml":          "db-02",
	} {
		t.Run(file, func(t *testing.T) {
			c := fakecluster.New(t, []string{filepath.Join("..", "..", "examples", file)})
			topo, err := Inspect(context.Background(), c, vm(vmName))
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range topo.Networks {
				if n.NAD == nil {
					continue
				}
				if n.NAD.Lookup != kube.StateFound || n.NAD.Config == nil || len(n.NAD.Config.Warnings) > 0 {
					t.Errorf("network %s: %+v", n.Name, n.NAD)
				}
			}
		})
	}
}
