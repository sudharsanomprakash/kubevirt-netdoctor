package diagnose

import (
	"fmt"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Finding IDs. IDs are part of the JSON contract and must stay stable.
const (
	IDAccessDenied            = "ACCESS_DENIED"
	IDAPIUnavailable          = "API_UNAVAILABLE"
	IDVMINotRunning           = "VMI_NOT_RUNNING"
	IDNADNotFound             = "NAD_NOT_FOUND"
	IDNADReferenceInvalid     = "NAD_REFERENCE_INVALID"
	IDNADConfigInvalid        = "NAD_CONFIG_INVALID"
	IDNADConfigSuspicious     = "NAD_CONFIG_SUSPICIOUS"
	IDNADConfigNotInline      = "NAD_CONFIG_NOT_INLINE"
	IDLauncherPodMissing      = "LAUNCHER_POD_MISSING"
	IDLauncherPodAmbiguous    = "LAUNCHER_POD_AMBIGUOUS"
	IDLauncherPodMultiple     = "LAUNCHER_POD_MULTIPLE"
	IDLauncherNodeMismatch    = "LAUNCHER_NODE_MISMATCH"
	IDMultusAnnotationInvalid = "MULTUS_ANNOTATION_INVALID"
	IDMultusRequestMissing    = "MULTUS_REQUEST_MISSING"
	IDMultusAttachmentMissing = "MULTUS_ATTACHMENT_MISSING"
	IDMultusStatusUnavailable = "MULTUS_STATUS_UNAVAILABLE"
	IDInterfaceMismatch       = "INTERFACE_STATUS_MISMATCH"
	IDGuestIPUnavailable      = "GUEST_IP_UNAVAILABLE"
)

// AllRules returns every rule, as run by "diagnose"; rules whose inputs are absent stay
// silent, so this is safe without a host snapshot or destination.
func AllRules() []Rule {
	rules := DefaultRules()
	rules = append(rules, HostRules()...)
	rules = append(rules, RouteRules()...)
	return append(rules, TopologyRules()...)
}

// DefaultRules returns the Kubernetes API rules evaluated by "inspect".
func DefaultRules() []Rule {
	return []Rule{
		{ID: "access", Description: "API requests that were denied or could not be answered", Check: onTopology(checkAccess)},
		{ID: "vmi-running", Description: "VMI existence and phase", Check: onTopology(checkVMIRunning)},
		{ID: "nad-exists", Description: "Referenced NetworkAttachmentDefinitions exist", Check: onTopology(checkNADExists)},
		{ID: "nad-config", Description: "NetworkAttachmentDefinition CNI configuration is valid", Check: onTopology(checkNADConfig)},
		{ID: "launcher-pod", Description: "A single virt-launcher pod hosts the VMI", Check: onTopology(checkLauncherPod)},
		{ID: "multus-attachments", Description: "Requested Multus attachments are reported in network-status", Check: onTopology(checkMultusAttachments)},
		{ID: "interface-status", Description: "VMI interface spec and status agree", Check: onTopology(checkInterfaceStatus)},
		{ID: "multus-mac", Description: "Multus applied the MAC address KubeVirt requested", Check: onTopology(checkAttachmentMAC)},
	}
}

func vmiResource(t *topology.Topology) string {
	return fmt.Sprintf("%s %s/%s", topology.KindVMI, t.Target.Namespace, instanceName(t))
}

func instanceName(t *topology.Topology) string {
	if t.Instance != nil && t.Instance.Name != "" {
		return t.Instance.Name
	}
	return t.Target.Name
}

func nadResource(ref multus.NetworkRef) string {
	return "NetworkAttachmentDefinition " + ref.String()
}

func podResource(t *topology.Topology, name string) string {
	return fmt.Sprintf("Pod %s/%s", t.Target.Namespace, name)
}

func vmiPhase(t *topology.Topology) string {
	if t.Instance == nil || t.Instance.Lookup != kube.StateFound {
		return ""
	}
	return t.Instance.Phase
}

func vmiRunning(t *topology.Topology) bool { return vmiPhase(t) == kubevirt.PhaseRunning }

func checkAccess(t *topology.Topology) []Finding {
	var out []Finding
	for _, a := range t.AccessIssues {
		res := a.Resource
		switch {
		case a.Namespace != "" && a.Name != "":
			res += " " + a.Namespace + "/" + a.Name
		case a.Namespace != "":
			res += " (namespace " + a.Namespace + ")"
		}
		evidence := []string{fmt.Sprintf("%s %s: %s", a.Verb, a.Resource, a.Message)}
		if a.State == kube.StateForbidden {
			out = append(out, Finding{
				ID: IDAccessDenied, Severity: SeverityWarning, Category: Unavailable, Resource: res,
				Summary:  fmt.Sprintf("Not permitted to %s %s; related checks were skipped", a.Verb, a.Resource),
				Evidence: evidence,
				NextAction: fmt.Sprintf("Grant %q on %s in namespace %q (see the RBAC section of the README) and re-run.",
					a.Verb, a.Resource, a.Namespace),
			})
			continue
		}
		summary := fmt.Sprintf("Could not %s %s; related checks were skipped", a.Verb, a.Resource)
		if a.Verb == "discover" {
			summary = fmt.Sprintf("The cluster does not serve %s; related checks were skipped", a.Resource)
			evidence = []string{a.Message}
		}
		out = append(out, Finding{
			ID: IDAPIUnavailable, Severity: SeverityWarning, Category: Unavailable, Resource: res,
			Summary:    summary,
			Evidence:   evidence,
			NextAction: "Verify the API is installed and reachable (kubectl api-resources), then re-run.",
		})
	}
	return out
}

func checkVMIRunning(t *topology.Topology) []Finding {
	inst := t.Instance
	if inst == nil {
		return nil
	}
	if inst.Lookup == kube.StateNotFound {
		return []Finding{vmiMissingFinding(t)}
	}
	if inst.Lookup != kube.StateFound || inst.Phase == kubevirt.PhaseRunning {
		return nil
	}

	f := Finding{
		ID:       IDVMINotRunning,
		Resource: vmiResource(t),
		Evidence: []string{"status.phase: " + displayOr(inst.Phase, "<unset>")},
	}
	if inst.NodeName != "" {
		f.Evidence = append(f.Evidence, "status.nodeName: "+inst.NodeName)
	}
	if inst.Reason != "" {
		f.Evidence = append(f.Evidence, "status.reason: "+inst.Reason)
	}
	f.Evidence = append(f.Evidence, falseConditions(inst.Conditions)...)

	switch inst.Phase {
	case kubevirt.PhaseFailed:
		f.Severity, f.Category = SeverityError, Confirmed
		f.Summary = "VMI has failed; runtime network state cannot be inspected"
		f.NextAction = "Inspect VMI conditions and virt-launcher pod events, e.g. kubectl get events -n " +
			t.Target.Namespace + " --field-selector involvedObject.name=" + inst.Name
	case kubevirt.PhaseSucceeded:
		f.Severity, f.Category = SeverityInfo, Confirmed
		f.Summary = "VMI has shut down; runtime network state cannot be inspected"
		f.NextAction = "Start the VM to inspect its runtime network attachments."
	default:
		f.Severity, f.Category = SeverityWarning, Suspected
		f.Summary = fmt.Sprintf("VMI is %s; network attachments may not be set up yet", displayOr(inst.Phase, "not yet scheduled"))
		f.NextAction = "If the phase does not progress, check the VMI conditions and virt-launcher pod events; " +
			"missing NetworkAttachmentDefinitions and unschedulable network resources commonly block startup."
	}
	return []Finding{f}
}

func vmiMissingFinding(t *topology.Topology) Finding {
	f := Finding{
		ID:       IDVMINotRunning,
		Resource: fmt.Sprintf("%s %s/%s", topology.KindVM, t.Target.Namespace, t.Target.Name),
		Evidence: []string{fmt.Sprintf("VirtualMachineInstance %s/%s does not exist", t.Target.Namespace, instanceName(t))},
	}
	strategy, printable := "", ""
	if vm := t.VirtualMachine; vm != nil {
		strategy, printable = vm.RunStrategy, vm.PrintableStatus
		f.Evidence = append(f.Evidence,
			"runStrategy: "+displayOr(strategy, "<unset>"),
			"printableStatus: "+displayOr(printable, "<unset>"))
	}
	f.Evidence = append(f.Evidence, "network configuration was evaluated from the VM template only")

	if strategy == kubevirt.RunStrategyHalted || strategy == kubevirt.RunStrategyManual || printable == "Stopped" {
		f.Severity, f.Category = SeverityInfo, Confirmed
		f.Summary = "VM is stopped; only its configured networks can be checked"
		f.NextAction = "Start the VM (virtctl start) to inspect runtime network attachments."
		return f
	}
	f.Severity, f.Category = SeverityWarning, Suspected
	f.Summary = "VM is expected to run but has no VMI"
	f.NextAction = "Check the VM status conditions (kubectl get vm -o yaml) and namespace events for start failures."
	return f
}

func checkNADExists(t *topology.Topology) []Finding {
	var out []Finding
	for _, group := range groupByNAD(t.Networks) {
		nad := group.nad
		switch {
		case nad.RefError != "":
			for _, n := range group.networks {
				out = append(out, Finding{
					ID: IDNADReferenceInvalid, Severity: SeverityError, Category: Confirmed,
					Resource:   vmiResource(t),
					Summary:    fmt.Sprintf("Network %q has an invalid multus.networkName", n),
					Evidence:   []string{nad.RefError},
					NextAction: "Use <name> or <namespace>/<name> in spec.networks[].multus.networkName.",
				})
			}
		case nad.Lookup == kube.StateNotFound:
			ev := []string{
				fmt.Sprintf("referenced by network(s): %s", strings.Join(group.networks, ", ")),
				fmt.Sprintf("GET %s %s returned NotFound", kube.ResourceString(kube.NetworkAttachmentDefinitionGVR), nad.Ref),
			}
			out = append(out, Finding{
				ID: IDNADNotFound, Severity: SeverityError, Category: Confirmed,
				Resource: nadResource(nad.Ref),
				Summary:  fmt.Sprintf("Referenced NetworkAttachmentDefinition %s does not exist", nad.Ref),
				Evidence: ev,
				NextAction: fmt.Sprintf("Create NetworkAttachmentDefinition %s, or correct spec.networks[].multus.networkName. "+
					"Names without a namespace resolve to the VM's namespace (%s).", nad.Ref, t.Target.Namespace),
			})
		}
	}
	return out
}

func checkNADConfig(t *topology.Topology) []Finding {
	var out []Finding
	for _, group := range groupByNAD(t.Networks) {
		nad := group.nad
		if nad.Lookup != kube.StateFound {
			continue
		}
		res := nadResource(nad.Ref)
		switch {
		case nad.ConfigError != "":
			out = append(out, Finding{
				ID: IDNADConfigInvalid, Severity: SeverityError, Category: Confirmed, Resource: res,
				Summary:  "NetworkAttachmentDefinition spec.config is not a valid CNI configuration",
				Evidence: []string{nad.ConfigError},
				NextAction: fmt.Sprintf("Fix spec.config; inspect it with: kubectl get network-attachment-definitions -n %s %s -o jsonpath='{.spec.config}'",
					nad.Ref.Namespace, nad.Ref.Name),
			})
		case nad.ConfigNotInline:
			out = append(out, Finding{
				ID: IDNADConfigNotInline, Severity: SeverityInfo, Category: Unavailable, Resource: res,
				Summary:    "CNI configuration is not inline; Multus reads it from a node-local file that netdoctor cannot observe",
				Evidence:   []string{"spec.config is empty"},
				NextAction: "Verify the CNI configuration file for this network exists on the node hosting the VM.",
			})
		case nad.Config != nil && len(nad.Config.Warnings) > 0:
			out = append(out, Finding{
				ID: IDNADConfigSuspicious, Severity: SeverityWarning, Category: Suspected, Resource: res,
				Summary:    "NetworkAttachmentDefinition CNI configuration parsed with warnings",
				Evidence:   nad.Config.Warnings,
				NextAction: "Review spec.config against the documentation of the CNI plugins it uses.",
			})
		}
	}
	return out
}

func checkLauncherPod(t *topology.Topology) []Finding {
	l := t.Launcher
	if l == nil || t.Instance == nil || t.Instance.Lookup != kube.StateFound {
		return nil
	}
	if l.Lookup != kube.StateFound && l.Lookup != kube.StateNotFound {
		return nil // access rule reports why
	}
	phase := t.Instance.Phase
	selector := fmt.Sprintf("selector %s=%s, owner VirtualMachineInstance %s", kubevirt.LabelCreatedBy, t.Instance.UID, t.Instance.Name)

	switch {
	case l.Selected == nil && len(l.Candidates) == 0:
		f := Finding{
			ID: IDLauncherPodMissing, Resource: vmiResource(t),
			Evidence: []string{selector, "VMI phase: " + displayOr(phase, "<unset>")},
		}
		for _, p := range l.Terminated {
			f.Evidence = append(f.Evidence, fmt.Sprintf("terminated launcher pod %s (phase %s)", p.Name, p.Phase))
		}
		switch phase {
		case kubevirt.PhaseScheduled, kubevirt.PhaseRunning:
			f.Severity, f.Category = SeverityError, Suspected
			f.Summary = "No active virt-launcher pod found for a " + phase + " VMI"
			f.NextAction = "Check that virt-launcher pods carry the KubeVirt labels and owner references, and inspect virt-controller logs."
		case kubevirt.PhaseFailed, kubevirt.PhaseSucceeded:
			return nil // expected; VMI_NOT_RUNNING covers it
		default:
			f.Severity, f.Category = SeverityInfo, Suspected
			f.Summary = "No virt-launcher pod exists yet for the VMI"
			f.NextAction = "Wait for scheduling; if it persists, check virt-controller events for pod creation failures."
		}
		return []Finding{f}

	case l.Selected == nil:
		f := Finding{
			ID: IDLauncherPodAmbiguous, Severity: SeverityWarning, Category: Suspected, Resource: vmiResource(t),
			Summary:    "Multiple active virt-launcher pods found; per-attachment checks were skipped",
			Evidence:   []string{l.SelectionReason},
			NextAction: "Re-run after any live migration completes, or inspect the pods individually.",
		}
		for _, p := range l.Candidates {
			f.Evidence = append(f.Evidence, fmt.Sprintf("candidate %s on node %s (phase %s)", p.Name, displayOr(p.NodeName, "<none>"), p.Phase))
		}
		if t.Instance.MigrationInProgress {
			f.Evidence = append(f.Evidence, "a live migration is in progress")
		}
		return []Finding{f}
	}

	var out []Finding
	if len(l.Candidates) > 1 {
		f := Finding{
			ID: IDLauncherPodMultiple, Severity: SeverityInfo, Category: Confirmed,
			Resource:   vmiResource(t),
			Summary:    fmt.Sprintf("%d active virt-launcher pods exist; attachment checks used %s", len(l.Candidates), l.Selected.Name),
			Evidence:   []string{l.SelectionReason},
			NextAction: "Re-run after the migration completes to inspect the final placement.",
		}
		if t.Instance.MigrationInProgress {
			f.Evidence = append(f.Evidence, "a live migration is in progress")
		}
		out = append(out, f)
	}
	if t.NodeName != "" && l.Selected.NodeName != "" && l.Selected.NodeName != t.NodeName {
		out = append(out, Finding{
			ID: IDLauncherNodeMismatch, Severity: SeverityWarning, Category: Suspected,
			Resource: podResource(t, l.Selected.Name),
			Summary:  "virt-launcher pod runs on a different node than the VMI reports",
			Evidence: []string{
				"VMI status.nodeName: " + t.NodeName,
				"launcher pod spec.nodeName: " + l.Selected.NodeName,
			},
			NextAction: "This is expected briefly during migration; otherwise check virt-handler on both nodes.",
		})
	}
	return out
}

func checkMultusAttachments(t *topology.Topology) []Finding {
	l := t.Launcher
	if l == nil || l.Selected == nil || l.Selected.Phase != "Running" {
		return nil
	}
	pod := podResource(t, l.Selected.Name)
	var out []Finding
	for _, msg := range []string{l.NetworksError, l.NetworkStatusError} {
		if msg != "" {
			out = append(out, Finding{
				ID: IDMultusAnnotationInvalid, Severity: SeverityWarning, Category: Suspected, Resource: pod,
				Summary:    "Multus annotation on the virt-launcher pod could not be parsed",
				Evidence:   []string{msg},
				NextAction: "Inspect the pod annotations; attachment checks relying on them were skipped.",
			})
		}
	}
	statusUsable := l.NetworkStatusPresent && l.NetworkStatusError == ""

	hasMultus := false
	for _, n := range t.Networks {
		if n.Source != topology.SourceMultus || n.NAD == nil || n.NAD.RefError != "" {
			continue
		}
		hasMultus = true
		if !n.MultusDefault && n.PodRequest == nil && l.NetworksError == "" {
			out = append(out, Finding{
				ID: IDMultusRequestMissing, Severity: SeverityWarning, Category: Suspected, Resource: pod,
				Summary:  fmt.Sprintf("Launcher pod does not request an attachment to %s for network %q", n.NAD.Ref, n.Name),
				Evidence: []string{fmt.Sprintf("no entry for %s in the %s annotation", n.NAD.Ref, multus.NetworksAnnotation)},
				NextAction: "Compare the VMI spec.networks with the launcher pod annotations; the pod may predate a spec change " +
					"(restart the VM to apply it).",
			})
		}
		if statusUsable && n.PodAttachment == nil {
			out = append(out, Finding{
				ID: IDMultusAttachmentMissing, Severity: SeverityError, Category: Confirmed, Resource: pod,
				Summary:  fmt.Sprintf("Attachment to %s (network %q) is not listed in the launcher pod's Multus network-status", n.NAD.Ref, n.Name),
				Evidence: attachmentEvidence(n, l),
				NextAction: "Check Multus and the delegate CNI plugin logs on node " + displayOr(l.Selected.NodeName, "<unknown>") +
					" and the pod events for CNI ADD errors.",
			})
		}
	}
	for _, n := range t.Networks {
		if n.Source == topology.SourcePod && statusUsable && n.PodAttachment == nil {
			out = append(out, Finding{
				ID: IDMultusAttachmentMissing, Severity: SeverityWarning, Category: Suspected, Resource: pod,
				Summary:    fmt.Sprintf("No default (cluster) network is listed in the launcher pod's Multus network-status for network %q", n.Name),
				Evidence:   []string{"no network-status entry has default=true"},
				NextAction: "Check the Multus cluster default network configuration.",
			})
		}
	}
	if hasMultus && !l.NetworkStatusPresent {
		out = append(out, Finding{
			ID: IDMultusStatusUnavailable, Severity: SeverityInfo, Category: Unavailable, Resource: pod,
			Summary:    "Multus network-status annotation is absent; secondary attachment state cannot be confirmed",
			Evidence:   []string{fmt.Sprintf("annotation %s not present on the launcher pod", multus.NetworkStatusAnnotation)},
			NextAction: "Verify that Multus is configured to publish network-status, or check attachments on the node directly.",
		})
	}
	return out
}

func attachmentEvidence(n topology.NetworkPath, l *topology.LauncherInfo) []string {
	ev := []string{fmt.Sprintf("%s annotation present on pod %s", multus.NetworkStatusAnnotation, l.Selected.Name)}
	if n.PodRequest != nil && n.PodRequest.Interface != "" {
		ev = append(ev, "requested pod interface: "+n.PodRequest.Interface)
	}
	if n.Guest != nil && n.Guest.PodInterfaceName != "" {
		ev = append(ev, "VMI status podInterfaceName: "+n.Guest.PodInterfaceName)
	}
	return ev
}

func checkInterfaceStatus(t *topology.Topology) []Finding {
	if !vmiRunning(t) {
		return nil
	}
	res := vmiResource(t)
	var out []Finding
	var noIP []string
	for _, n := range t.Networks {
		switch {
		case n.Interface == nil:
			out = append(out, Finding{
				ID: IDInterfaceMismatch, Severity: SeverityError, Category: Confirmed, Resource: res,
				Summary:    fmt.Sprintf("Network %q has no interface with the same name in spec.domain.devices.interfaces", n.Name),
				Evidence:   []string{"KubeVirt pairs networks and interfaces by name"},
				NextAction: "Add a matching interface or remove the unused network.",
			})
		case n.Guest == nil:
			out = append(out, Finding{
				ID: IDInterfaceMismatch, Severity: SeverityWarning, Category: Suspected, Resource: res,
				Summary:    fmt.Sprintf("Interface %q is defined in the VMI spec but not reported in VMI status", n.Name),
				Evidence:   []string{"binding: " + n.Interface.Binding, "VMI phase: Running"},
				NextAction: "Check virt-handler logs on node " + displayOr(t.NodeName, "<unknown>") + " and the guest agent status.",
			})
		default:
			if n.Interface.MAC != "" && n.Guest.MAC != "" && !strings.EqualFold(n.Interface.MAC, n.Guest.MAC) {
				out = append(out, Finding{
					ID: IDInterfaceMismatch, Severity: SeverityWarning, Category: Confirmed, Resource: res,
					Summary: fmt.Sprintf("Interface %q reports MAC %s but the spec requests %s", n.Name, n.Guest.MAC, n.Interface.MAC),
					Evidence: []string{
						"spec macAddress: " + n.Interface.MAC,
						fmt.Sprintf("status mac: %s (source: %s)", n.Guest.MAC, displayOr(n.Guest.InfoSource, "unknown")),
					},
					NextAction: "Check whether the guest OS overrides the MAC address, or whether the spec changed without a restart.",
				})
			}
			if len(n.Guest.IPs) == 0 {
				noIP = append(noIP, n.Name)
			}
		}
	}
	for _, g := range t.UnmatchedGuestInterfaces {
		out = append(out, Finding{
			ID: IDInterfaceMismatch, Severity: SeverityWarning, Category: Suspected, Resource: res,
			Summary:    fmt.Sprintf("VMI status reports interface %q that is not defined in the VMI spec", g.Name),
			Evidence:   []string{"guest interface: " + displayOr(g.InterfaceName, "<unknown>"), "source: " + displayOr(g.InfoSource, "unknown")},
			NextAction: "The spec may have changed without a restart; restart the VM to apply the current spec.",
		})
	}
	if len(noIP) > 0 && t.Instance.GuestAgent != topology.AgentConnected {
		out = append(out, Finding{
			ID: IDGuestIPUnavailable, Severity: SeverityInfo, Category: Unavailable, Resource: res,
			Summary: "Guest IP addresses are not reported; the guest agent is not connected",
			Evidence: []string{
				"interfaces without IPs: " + strings.Join(noIP, ", "),
				"guest agent: " + t.Instance.GuestAgent,
			},
			NextAction: "Install and start qemu-guest-agent in the guest to report in-guest addresses.",
		})
	}
	return out
}

type nadGroup struct {
	nad      *topology.NADInfo
	networks []string
}

// groupByNAD groups Multus networks by referenced NAD, preserving spec order,
// so a NAD used by several networks yields one finding.
func groupByNAD(paths []topology.NetworkPath) []nadGroup {
	var groups []nadGroup
	index := map[string]int{}
	for _, p := range paths {
		if p.NAD == nil {
			continue
		}
		key := p.NAD.Ref.String()
		if p.NAD.RefError != "" {
			key = "invalid:" + p.Name
		}
		if i, ok := index[key]; ok {
			groups[i].networks = append(groups[i].networks, p.Name)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, nadGroup{nad: p.NAD, networks: []string{p.Name}})
	}
	return groups
}

func falseConditions(conds []kubevirt.Condition) []string {
	var out []string
	for _, c := range conds {
		if c.Status == "True" {
			continue
		}
		s := fmt.Sprintf("condition %s=%s", c.Type, c.Status)
		if c.Reason != "" {
			s += " reason=" + c.Reason
		}
		if c.Message != "" {
			s += ": " + c.Message
		}
		out = append(out, s)
	}
	return out
}

func displayOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
