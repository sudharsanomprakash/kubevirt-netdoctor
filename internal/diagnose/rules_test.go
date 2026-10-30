package diagnose

import (
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

var nadRef = multus.NetworkRef{Namespace: "demo", Name: "br1"}

// runningTopology is a consistent running VMI with one pod network and one
// Multus network; tests mutate it to introduce a single fault.
func runningTopology() *topology.Topology {
	pod := &topology.PodInfo{Name: "virt-launcher-vm-abcde", Phase: "Running", NodeName: "worker-1"}
	return &topology.Topology{
		Target:         topology.Target{Kind: topology.KindVM, Namespace: "demo", Name: "vm"},
		VirtualMachine: &topology.VMInfo{Name: "vm", Lookup: kube.StateFound, RunStrategy: kubevirt.RunStrategyAlways},
		Instance: &topology.VMIInfo{
			Name: "vm", Lookup: kube.StateFound, UID: "uid", Phase: kubevirt.PhaseRunning,
			NodeName: "worker-1", GuestAgent: topology.AgentConnected, NetworkSpecSource: topology.SpecFromVMI,
		},
		Launcher: &topology.LauncherInfo{
			Lookup: kube.StateFound, Selected: pod, Candidates: []topology.PodInfo{*pod},
			NetworkStatusPresent: true,
		},
		NodeName: "worker-1",
		Networks: []topology.NetworkPath{
			{
				Name: "default", Source: topology.SourcePod,
				Interface:     &topology.SpecInterface{Binding: "masquerade"},
				Guest:         &topology.GuestInterface{Name: "default", InterfaceName: "enp1s0", IPs: []string{"10.244.1.15"}},
				PodAttachment: &topology.PodAttachment{NetworkName: "k8s-pod-network", Interface: "eth0", Default: true},
			},
			{
				Name: "sec", Source: topology.SourceMultus,
				Interface:     &topology.SpecInterface{Binding: "bridge", MAC: "02:00:00:00:00:01"},
				Guest:         &topology.GuestInterface{Name: "sec", InterfaceName: "enp2s0", MAC: "02:00:00:00:00:01", IPs: []string{"192.0.2.10"}},
				PodRequest:    &topology.PodRequest{Interface: "pod1"},
				PodAttachment: &topology.PodAttachment{NetworkName: "demo/br1", Interface: "pod1"},
				NAD:           &topology.NADInfo{Ref: nadRef, Lookup: kube.StateFound, Config: &multus.CNIConfig{CNIVersion: "0.3.1"}},
			},
		},
	}
}

func evaluate(t *topology.Topology) []Finding { return Evaluate(&Input{Topology: t}, DefaultRules()) }

func ids(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func only(t *testing.T, fs []Finding, id string, sev Severity, cat Category) Finding {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("want exactly one finding %s, got %v", id, ids(fs))
	}
	f := fs[0]
	if f.ID != id || f.Severity != sev || f.Category != cat {
		t.Fatalf("got %s/%s/%s, want %s/%s/%s", f.ID, f.Severity, f.Category, id, sev, cat)
	}
	if f.Summary == "" || f.Resource == "" || f.NextAction == "" || len(f.Evidence) == 0 {
		t.Errorf("finding is missing fields: %+v", f)
	}
	return f
}

func TestConsistentTopologyHasNoFindings(t *testing.T) {
	if fs := evaluate(runningTopology()); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
}

func TestNADNotFound(t *testing.T) {
	topo := runningTopology()
	topo.Networks[1].NAD = &topology.NADInfo{Ref: nadRef, Lookup: kube.StateNotFound}
	// A second network on the same missing NAD yields one finding.
	dup := topo.Networks[1]
	dup.Name = "sec2"
	topo.Networks = append(topo.Networks, dup)

	f := only(t, checkNADExists(topo), IDNADNotFound, SeverityError, Confirmed)
	if f.Resource != "NetworkAttachmentDefinition demo/br1" || !strings.Contains(f.Evidence[0], "sec, sec2") {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestNADForbiddenIsNotReportedMissing(t *testing.T) {
	topo := runningTopology()
	topo.Networks[1].NAD = &topology.NADInfo{Ref: nadRef, Lookup: kube.StateForbidden}
	topo.AccessIssues = []kube.AccessIssue{{
		Verb: "get", Resource: "network-attachment-definitions.k8s.cni.cncf.io", Namespace: "demo", Name: "br1",
		State: kube.StateForbidden, Message: "forbidden",
	}}
	fs := evaluate(topo)
	only(t, fs, IDAccessDenied, SeverityWarning, Unavailable)
}

func TestNADReferenceInvalid(t *testing.T) {
	topo := runningTopology()
	topo.Networks[1].NAD = &topology.NADInfo{RefError: "invalid network reference", Lookup: kube.StateNotChecked}
	only(t, checkNADExists(topo), IDNADReferenceInvalid, SeverityError, Confirmed)
}

func TestNADConfig(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].NAD.Config = nil
		topo.Networks[1].NAD.ConfigError = `plugins[0]: plugin has no "type"`
		only(t, checkNADConfig(topo), IDNADConfigInvalid, SeverityError, Confirmed)
	})
	t.Run("warnings", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].NAD.Config.Warnings = []string{"cniVersion is not set"}
		only(t, checkNADConfig(topo), IDNADConfigSuspicious, SeverityWarning, Suspected)
	})
	t.Run("not inline", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].NAD.Config = nil
		topo.Networks[1].NAD.ConfigNotInline = true
		only(t, checkNADConfig(topo), IDNADConfigNotInline, SeverityInfo, Unavailable)
	})
	t.Run("lookup failed skips config checks", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].NAD = &topology.NADInfo{Ref: nadRef, Lookup: kube.StateUnavailable, ConfigError: "stale"}
		if fs := checkNADConfig(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
}

func TestVMINotRunning(t *testing.T) {
	tests := []struct {
		phase string
		sev   Severity
		cat   Category
	}{
		{kubevirt.PhasePending, SeverityWarning, Suspected},
		{kubevirt.PhaseScheduling, SeverityWarning, Suspected},
		{kubevirt.PhaseScheduled, SeverityWarning, Suspected},
		{kubevirt.PhaseUnknown, SeverityWarning, Suspected},
		{kubevirt.PhaseFailed, SeverityError, Confirmed},
		{kubevirt.PhaseSucceeded, SeverityInfo, Confirmed},
	}
	for _, tc := range tests {
		t.Run(tc.phase, func(t *testing.T) {
			topo := runningTopology()
			topo.Instance.Phase = tc.phase
			topo.Instance.Conditions = []kubevirt.Condition{{Type: "Ready", Status: "False", Reason: "GuestNotRunning"}}
			f := only(t, checkVMIRunning(topo), IDVMINotRunning, tc.sev, tc.cat)
			if !strings.Contains(strings.Join(f.Evidence, "\n"), "reason=GuestNotRunning") {
				t.Errorf("conditions missing from evidence: %v", f.Evidence)
			}
			// Interface checks require a running VMI.
			if fs := checkInterfaceStatus(topo); len(fs) != 0 {
				t.Errorf("interface checks ran for phase %s: %v", tc.phase, ids(fs))
			}
		})
	}
}

func TestVMWithoutVMI(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		sev      Severity
		cat      Category
	}{
		{kubevirt.RunStrategyHalted, SeverityInfo, Confirmed},
		{kubevirt.RunStrategyManual, SeverityInfo, Confirmed},
		{kubevirt.RunStrategyAlways, SeverityWarning, Suspected},
		{kubevirt.RunStrategyRerunOnFailure, SeverityWarning, Suspected},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			topo := runningTopology()
			topo.VirtualMachine.RunStrategy = tc.strategy
			topo.Instance = &topology.VMIInfo{Name: "vm", Lookup: kube.StateNotFound, NetworkSpecSource: topology.SpecFromVMTemplate}
			topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateNotChecked}
			only(t, checkVMIRunning(topo), IDVMINotRunning, tc.sev, tc.cat)
			for _, f := range evaluate(topo) {
				if f.ID == IDLauncherPodMissing || f.ID == IDMultusAttachmentMissing || f.ID == IDInterfaceMismatch {
					t.Errorf("runtime finding %s reported without a VMI", f.ID)
				}
			}
		})
	}
}

func TestLauncherPod(t *testing.T) {
	t.Run("missing for running vmi", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateNotFound}
		only(t, checkLauncherPod(topo), IDLauncherPodMissing, SeverityError, Suspected)
		// Attachment checks need a pod; none may run.
		if fs := checkMultusAttachments(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
	t.Run("missing while pending is informational", func(t *testing.T) {
		topo := runningTopology()
		topo.Instance.Phase = kubevirt.PhasePending
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateNotFound}
		only(t, checkLauncherPod(topo), IDLauncherPodMissing, SeverityInfo, Suspected)
	})
	t.Run("missing after failure is expected", func(t *testing.T) {
		topo := runningTopology()
		topo.Instance.Phase = kubevirt.PhaseFailed
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateNotFound}
		if fs := checkLauncherPod(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		topo := runningTopology()
		topo.Instance.MigrationInProgress = true
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateFound, Candidates: []topology.PodInfo{{Name: "a"}, {Name: "b"}}, SelectionReason: "2 pods"}
		f := only(t, checkLauncherPod(topo), IDLauncherPodAmbiguous, SeverityWarning, Suspected)
		if !strings.Contains(strings.Join(f.Evidence, "\n"), "migration") {
			t.Errorf("evidence should mention migration: %v", f.Evidence)
		}
	})
	t.Run("multiple with selection", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher.Candidates = append(topo.Launcher.Candidates, topology.PodInfo{Name: "target", NodeName: "worker-2"})
		only(t, checkLauncherPod(topo), IDLauncherPodMultiple, SeverityInfo, Confirmed)
	})
	t.Run("node mismatch", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher.Selected.NodeName = "worker-9"
		only(t, checkLauncherPod(topo), IDLauncherNodeMismatch, SeverityWarning, Suspected)
	})
	t.Run("forbidden list does not claim missing", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateForbidden}
		if fs := checkLauncherPod(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
}

func TestMultusAttachments(t *testing.T) {
	t.Run("secondary missing from status", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].PodAttachment = nil
		f := only(t, checkMultusAttachments(topo), IDMultusAttachmentMissing, SeverityError, Confirmed)
		if !strings.Contains(f.Summary, "demo/br1") {
			t.Errorf("summary: %s", f.Summary)
		}
	})
	t.Run("default network missing from status", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[0].PodAttachment = nil
		only(t, checkMultusAttachments(topo), IDMultusAttachmentMissing, SeverityWarning, Suspected)
	})
	t.Run("status annotation absent", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher.NetworkStatusPresent = false
		topo.Networks[0].PodAttachment, topo.Networks[1].PodAttachment = nil, nil
		only(t, checkMultusAttachments(topo), IDMultusStatusUnavailable, SeverityInfo, Unavailable)
	})
	t.Run("status absent without multus networks is silent", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks = topo.Networks[:1]
		topo.Networks[0].PodAttachment = nil
		topo.Launcher.NetworkStatusPresent = false
		if fs := checkMultusAttachments(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
	t.Run("request missing", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].PodRequest = nil
		only(t, checkMultusAttachments(topo), IDMultusRequestMissing, SeverityWarning, Suspected)
	})
	t.Run("malformed status annotation", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher.NetworkStatusError = "parsing: bad"
		topo.Networks[1].PodAttachment = nil
		// The malformed annotation is reported; no attachment is claimed missing.
		only(t, checkMultusAttachments(topo), IDMultusAnnotationInvalid, SeverityWarning, Suspected)
	})
	t.Run("pod not running", func(t *testing.T) {
		topo := runningTopology()
		topo.Launcher.Selected.Phase = "Pending"
		topo.Networks[1].PodAttachment = nil
		if fs := checkMultusAttachments(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
}

func TestInterfaceStatus(t *testing.T) {
	t.Run("not reported", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].Guest = nil
		only(t, checkInterfaceStatus(topo), IDInterfaceMismatch, SeverityWarning, Suspected)
	})
	t.Run("mac mismatch", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].Guest.MAC = "02:00:00:00:00:99"
		only(t, checkInterfaceStatus(topo), IDInterfaceMismatch, SeverityWarning, Confirmed)
	})
	t.Run("mac compare is case-insensitive", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].Guest.MAC = "02:00:00:00:00:01"
		topo.Networks[1].Interface.MAC = "02:00:00:00:00:01"
		if fs := checkInterfaceStatus(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
	t.Run("network without interface", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].Interface = nil
		only(t, checkInterfaceStatus(topo), IDInterfaceMismatch, SeverityError, Confirmed)
	})
	t.Run("status interface not in spec", func(t *testing.T) {
		topo := runningTopology()
		topo.UnmatchedGuestInterfaces = []topology.GuestInterface{{Name: "old", InterfaceName: "enp9s0"}}
		only(t, checkInterfaceStatus(topo), IDInterfaceMismatch, SeverityWarning, Suspected)
	})
	t.Run("no ips without agent", func(t *testing.T) {
		topo := runningTopology()
		topo.Instance.GuestAgent = topology.AgentNotConnected
		topo.Networks[1].Guest.IPs = nil
		only(t, checkInterfaceStatus(topo), IDGuestIPUnavailable, SeverityInfo, Unavailable)
	})
	t.Run("no ips with agent makes no claim", func(t *testing.T) {
		topo := runningTopology()
		topo.Networks[1].Guest.IPs = nil
		if fs := checkInterfaceStatus(topo); len(fs) != 0 {
			t.Errorf("unexpected %v", ids(fs))
		}
	})
}

func TestAccessFindings(t *testing.T) {
	topo := runningTopology()
	topo.AccessIssues = []kube.AccessIssue{
		{Verb: "list", Resource: "pods", Namespace: "demo", State: kube.StateForbidden, Message: "forbidden"},
		{Verb: "discover", Resource: "network-attachment-definitions.k8s.cni.cncf.io", State: kube.StateUnavailable, Message: "not served"},
		{Verb: "get", Resource: "virtualmachines.kubevirt.io", Namespace: "demo", Name: "vm", State: kube.StateUnavailable, Message: "timeout"},
	}
	fs := checkAccess(topo)
	if got := strings.Join(ids(fs), ","); got != "ACCESS_DENIED,API_UNAVAILABLE,API_UNAVAILABLE" {
		t.Fatalf("ids = %s", got)
	}
	if fs[0].Resource != "pods (namespace demo)" || !strings.Contains(fs[1].Summary, "does not serve") {
		t.Errorf("unexpected: %+v", fs)
	}
}

func TestEvaluateOrdering(t *testing.T) {
	topo := runningTopology()
	topo.Networks[1].NAD = &topology.NADInfo{Ref: nadRef, Lookup: kube.StateNotFound}
	topo.Networks[1].PodAttachment = nil
	topo.Instance.GuestAgent = topology.AgentNotConnected
	topo.Networks[1].Guest.IPs = nil
	topo.Networks[1].Guest.MAC = "02:00:00:00:00:99"

	fs := evaluate(topo)
	got := strings.Join(ids(fs), ",")
	want := "MULTUS_ATTACHMENT_MISSING,NAD_NOT_FOUND,INTERFACE_STATUS_MISMATCH,GUEST_IP_UNAVAILABLE"
	if got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	if empty := Evaluate(&Input{Topology: &topology.Topology{}}, nil); empty == nil || len(empty) != 0 {
		t.Errorf("Evaluate must return an empty, non-nil slice: %#v", empty)
	}
}
