package diagnose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/graph"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Finding IDs for attachment, host, OVS and topology rules.
const (
	IDMultusAttachmentMismatch = "MULTUS_ATTACHMENT_MISMATCH"
	IDHostSnapshotNodeMismatch = "HOST_SNAPSHOT_NODE_MISMATCH"
	IDHostSnapshotHostname     = "HOST_SNAPSHOT_HOSTNAME_MISMATCH"
	IDHostCollectorUnavailable = "HOST_COLLECTOR_UNAVAILABLE"
	IDHostInterfaceMissing     = "HOST_INTERFACE_MISSING"
	IDHostInterfaceDown        = "HOST_INTERFACE_DOWN"
	IDOVSPortMissing           = "OVS_PORT_MISSING"
	IDOVSPortWrongBridge       = "OVS_PORT_WRONG_BRIDGE"
	IDOVSBridgeNoFlows         = "OVS_BRIDGE_NO_FLOWS"
	IDVLANMismatch             = "VLAN_MISMATCH"
	IDVLANNotEnforced          = "VLAN_NOT_ENFORCED"
	IDMTUMismatch              = "MTU_MISMATCH"
	IDBondMemberDetached       = "BOND_MEMBER_DETACHED"
	IDLACPPartnerMismatch      = "LACP_PARTNER_MISMATCH"
	IDTopologyIncomplete       = "TOPOLOGY_INCOMPLETE"
)

// HostRules evaluate a host snapshot correlated with the topology.
func HostRules() []Rule {
	return []Rule{
		{ID: "host-snapshot", Description: "The host snapshot describes the VM's node and was fully collected", Check: checkHostSnapshot},
		{ID: "host-interface", Description: "Host devices declared by NADs exist and are up", Check: checkHostInterfaces},
		{ID: "ovs-port", Description: "The OVS CNI port of each attachment is on the declared bridge", Check: checkOVSPort},
		{ID: "ovs-flows", Description: "OVS bridges in secure fail mode have flows", Check: checkOVSFlows},
		{ID: "vlan", Description: "VLAN configuration agrees along the path", Check: checkVLAN},
		{ID: "mtu", Description: "Host devices support the declared MTU", Check: checkMTU},
		{ID: "bond", Description: "Bond members are attached and agree on the LACP partner", Check: checkBonds},
	}
}

// TopologyRules report gaps in the correlated topology.
func TopologyRules() []Rule {
	return []Rule{{ID: "topology", Description: "Layers that could not be observed", Check: checkTopologyGaps}}
}

func checkAttachmentMAC(t *topology.Topology) []Finding {
	var out []Finding
	for _, n := range t.Networks {
		if n.PodRequest == nil || n.PodAttachment == nil || n.PodRequest.MAC == "" || n.PodAttachment.MAC == "" {
			continue
		}
		if strings.EqualFold(n.PodRequest.MAC, n.PodAttachment.MAC) {
			continue
		}
		out = append(out, Finding{
			ID: IDMultusAttachmentMismatch, Severity: SeverityWarning, Category: Suspected,
			Resource: podResource(t, t.Launcher.Selected.Name),
			Summary:  fmt.Sprintf("Network %q: the CNI did not apply the MAC address KubeVirt requested", n.Name),
			Evidence: []string{
				fmt.Sprintf("requested in %s: %s", multus.NetworksAnnotation, n.PodRequest.MAC),
				fmt.Sprintf("reported in %s: %s (interface %s)", multus.NetworkStatusAnnotation, n.PodAttachment.MAC, n.PodAttachment.Interface),
			},
			Expected:   n.PodRequest.MAC + " (Multus request)",
			NextAction: "Check that the NAD's plugin chain supports the \"mac\" capability (for example by chaining the tuning plugin).",
		})
	}
	return out
}

func snapshotResource(in *Input) string {
	return "Node " + in.Snapshot.NodeName + " (host snapshot)"
}

func checkHostSnapshot(in *Input) []Finding {
	if in.Snapshot == nil || in.Host == nil {
		return nil
	}
	if !in.Host.NodeMatch {
		return []Finding{{
			ID: IDHostSnapshotNodeMismatch, Severity: SeverityWarning, Category: Unavailable,
			Resource: snapshotResource(in),
			Summary:  "The host snapshot is not from the VM's node; host checks were skipped",
			Evidence: []string{
				"snapshot nodeName: " + displayOr(in.Host.SnapshotNode, "<unset>"),
				"VMI status.nodeName: " + displayOr(in.Host.VMINode, "<unknown>"),
			},
			Expected:   "snapshot of node " + displayOr(in.Host.VMINode, "<unknown>"),
			NextAction: "Run kubectl netdoctor host collect on the node hosting the VMI (pass --node-name if it differs from the hostname).",
		}}
	}
	var out []Finding
	if !host.NamesAgree(in.Snapshot.NodeName, in.Snapshot.Hostname) {
		out = append(out, Finding{
			ID: IDHostSnapshotHostname, Severity: SeverityWarning, Category: Suspected,
			Resource: snapshotResource(in),
			Summary:  "The host snapshot was collected on a host whose name differs from its node name; host findings may describe another machine",
			Evidence: []string{
				"snapshot nodeName: " + in.Snapshot.NodeName,
				"snapshot hostname: " + in.Snapshot.Hostname,
			},
			Expected:   "hostname matching the node name (unless the kubelet overrides the node name)",
			NextAction: "Confirm the collector ran on node " + in.Snapshot.NodeName + "; if not, collect again on that node.",
		})
	}
	for _, src := range in.Snapshot.Sources {
		if src.Status != host.StatusFailed {
			continue
		}
		ev := []string{src.Message}
		if len(src.Command) > 0 {
			ev = append(ev, "command: "+strings.Join(src.Command, " "))
		}
		out = append(out, Finding{
			ID: IDHostCollectorUnavailable, Severity: SeverityWarning, Category: Unavailable,
			Resource:   snapshotResource(in),
			Summary:    fmt.Sprintf("Host source %q could not be collected; checks depending on it were skipped", src.Name),
			Evidence:   ev,
			NextAction: "Re-run the host collector with the privileges described in the README's host collector section.",
		})
	}
	return out
}

// hostPaths returns the correlated host paths, or nil when host checks
// cannot run.
func hostPaths(in *Input) []hostpath.NetworkPath {
	if in.Host == nil || !in.Host.NodeMatch {
		return nil
	}
	return in.Host.Paths
}

func networkByName(t *topology.Topology, name string) *topology.NetworkPath {
	for i := range t.Networks {
		if t.Networks[i].Name == name {
			return &t.Networks[i]
		}
	}
	return nil
}

func nadOf(t *topology.Topology, network string) string {
	if n := networkByName(t, network); n != nil && n.NAD != nil {
		return n.NAD.Ref.String()
	}
	return "<unknown NAD>"
}

func checkHostInterfaces(in *Input) []Finding {
	var out []Finding
	for _, p := range hostPaths(in) {
		res := snapshotResource(in)
		switch {
		case p.DeviceState == hostpath.Missing:
			ev := []string{
				fmt.Sprintf("NAD %s declares %q (network %q)", nadOf(in.Topology, p.Network), p.DeclaredDevice, p.Network),
				p.DeviceReason,
			}
			out = append(out, Finding{
				ID: IDHostInterfaceMissing, Severity: SeverityError, Category: Confirmed, Resource: res,
				Summary:    fmt.Sprintf("Host device %q required by network %q does not exist on node %s", p.DeclaredDevice, p.Network, in.Snapshot.NodeName),
				Evidence:   ev,
				Expected:   fmt.Sprintf("%s %q present (NAD configuration)", deviceNoun(p.Kind), p.DeclaredDevice),
				NextAction: "Create the device on the node (for example with kubernetes-nmstate) or fix the NAD; check node selection for this network.",
			})
		case p.DeviceState == hostpath.Found && p.Device.AdminUp != nil && (!*p.Device.AdminUp || p.Device.OperState == "DOWN"):
			out = append(out, Finding{
				ID: IDHostInterfaceDown, Severity: SeverityError, Category: Confirmed, Resource: res,
				Summary:    fmt.Sprintf("Host device %q used by network %q is down", p.Device.Name, p.Network),
				Evidence:   []string{fmt.Sprintf("admin up: %v, operstate: %s", *p.Device.AdminUp, p.Device.OperState)},
				Expected:   "device up",
				NextAction: "Inspect the device with ip link show " + p.Device.Name + " on the node.",
			})
		}
		if p.DeviceState != hostpath.Found || len(p.Uplinks) == 0 {
			continue
		}
		var down, up []string
		for _, u := range p.Uplinks {
			if u.Down {
				down = append(down, fmt.Sprintf("%s (%s)", u.Name, displayOr(u.OperState, "down")))
			} else {
				up = append(up, u.Name)
			}
		}
		if len(down) == 0 {
			continue
		}
		sev := SeverityWarning
		summary := fmt.Sprintf("Some uplinks of %q are down", p.Device.Name)
		if len(up) == 0 {
			sev, summary = SeverityError, fmt.Sprintf("All uplinks of %q are down; network %q has no path off the node", p.Device.Name, p.Network)
		}
		ev := []string{"down: " + strings.Join(down, ", ")}
		if len(up) > 0 {
			ev = append(ev, "up: "+strings.Join(up, ", "))
		}
		out = append(out, Finding{
			ID: IDHostInterfaceDown, Severity: sev, Category: Confirmed, Resource: res,
			Summary: summary, Evidence: ev, Expected: "uplinks up",
			NextAction: "Check cabling, the switch port and the interface state on the node.",
		})
	}
	return out
}

func deviceNoun(kind string) string {
	switch kind {
	case hostpath.KindLinuxBridge:
		return "Linux bridge"
	case hostpath.KindOVS:
		return "OVS bridge"
	default:
		return "interface"
	}
}

func checkOVSPort(in *Input) []Finding {
	var out []Finding
	for _, p := range hostPaths(in) {
		if p.Kind != hostpath.KindOVS || p.DeviceState == hostpath.Missing {
			continue
		}
		n := networkByName(in.Topology, p.Network)
		switch p.PodPortState {
		case hostpath.Missing:
			f := Finding{
				ID: IDOVSPortMissing, Resource: snapshotResource(in),
				Summary: fmt.Sprintf("No OVS port created by OVS CNI for network %q of the launcher pod exists", p.Network),
				Evidence: []string{fmt.Sprintf("searched Port external_ids %s=%s, %s=%s",
					host.ExternalIDOwner, host.OVSCNIOwner, host.ExternalIDPodUID, in.Topology.Launcher.Selected.UID)},
				Expected:   fmt.Sprintf("an OVS CNI port on bridge %q (NAD configuration)", p.DeclaredDevice),
				NextAction: "Check OVS CNI and kubelet logs on the node for CNI ADD/DEL errors; the port may have been removed after the pod started.",
			}
			if n != nil && n.PodAttachment != nil {
				f.Severity, f.Category = SeverityError, Confirmed
				f.Evidence = append(f.Evidence, fmt.Sprintf("Multus reports pod interface %s for this network", n.PodAttachment.Interface))
			} else {
				f.Severity, f.Category = SeverityWarning, Suspected
				f.Evidence = append(f.Evidence, "Multus does not report this attachment either")
			}
			out = append(out, f)
		case hostpath.WrongBridge:
			out = append(out, Finding{
				ID: IDOVSPortWrongBridge, Severity: SeverityError, Category: Confirmed, Resource: snapshotResource(in),
				Summary:    fmt.Sprintf("The OVS port of network %q is on bridge %q, not %q", p.Network, p.PodPort.Bridge, p.DeclaredDevice),
				Evidence:   []string{fmt.Sprintf("port %s matched by %s", p.PodPort.Name, p.PodPort.MatchedBy)},
				Expected:   fmt.Sprintf("port on bridge %q (NAD configuration)", p.DeclaredDevice),
				NextAction: "Check whether the NAD changed after the pod started; restart the VM to re-create the attachment.",
			})
		}
	}
	return out
}

func checkOVSFlows(in *Input) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, p := range hostPaths(in) {
		d := p.Device
		if p.Kind != hostpath.KindOVS || d == nil || d.Flows == nil || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		if d.FailMode == "secure" && d.Flows.Count == 0 {
			out = append(out, Finding{
				ID: IDOVSBridgeNoFlows, Severity: SeverityWarning, Category: Confirmed, Resource: snapshotResource(in),
				Summary: fmt.Sprintf("OVS bridge %q is in secure fail mode and has no OpenFlow flows", d.Name),
				Evidence: []string{
					"fail_mode: secure",
					"ovs-ofctl dump-flows returned 0 flows",
					"in secure mode OVS does not fall back to normal switching, so a bridge without flows drops traffic",
				},
				Expected:   "flows installed by the bridge's controller, or fail_mode standalone",
				NextAction: "Check the controller that should program this bridge (ovs-vsctl get-controller " + d.Name + ").",
			})
		}
	}
	return out
}

// declaredVLAN returns the VLAN to check for a path and where it came from:
// the NAD config, or the user's expectations when the NAD declares none.
func declaredVLAN(in *Input, p hostpath.NetworkPath) (int, string, bool) {
	if p.DeclaredVLAN != nil && *p.DeclaredVLAN > 0 {
		return *p.DeclaredVLAN, "NAD configuration", true
	}
	if e, ok := in.Expect.Network(p.Network); ok && e.VLAN != nil && *e.VLAN > 0 {
		return *e.VLAN, "expectations file", true
	}
	return 0, "", false
}

func checkVLAN(in *Input) []Finding {
	var out []Finding
	res := func() string { return snapshotResource(in) }

	// Expectations vs NAD configuration; needs no host snapshot.
	for _, n := range in.Topology.Networks {
		e, ok := in.Expect.Network(n.Name)
		if !ok || e.VLAN == nil || n.NAD == nil || n.NAD.Config == nil || n.NAD.Config.Primary() == nil {
			continue
		}
		prim := n.NAD.Config.Primary()
		declared := 0
		if prim.VLAN != nil {
			declared = *prim.VLAN
		}
		if declared != *e.VLAN && (declared != 0 || !prim.AllowsVLAN(*e.VLAN)) {
			out = append(out, Finding{
				ID: IDVLANMismatch, Severity: SeverityError, Category: Confirmed, Resource: nadResource(n.NAD.Ref),
				Summary:    fmt.Sprintf("Network %q: NAD %s configures VLAN %d, but VLAN %d is expected", n.Name, n.NAD.Ref, declared, *e.VLAN),
				Evidence:   []string{"NAD config: " + n.NAD.Config.Summary()},
				Expected:   fmt.Sprintf("VLAN %d (expectations file)", *e.VLAN),
				NextAction: "Correct the NAD's \"vlan\" or the expectations file.",
			})
		}
	}

	for _, p := range hostPaths(in) {
		vlan, source, ok := declaredVLAN(in, p)
		if p.Kind == hostpath.KindOVS && p.PodPort != nil {
			pp := p.PodPort
			if p.DeclaredVLAN != nil && *p.DeclaredVLAN > 0 && (pp.Tag == nil || *pp.Tag != *p.DeclaredVLAN) {
				out = append(out, Finding{
					ID: IDVLANMismatch, Severity: SeverityError, Category: Confirmed, Resource: res(),
					Summary:    fmt.Sprintf("OVS port %s of network %q has tag %s", pp.Name, p.Network, intOr(pp.Tag, "none")),
					Evidence:   []string{portVLANString(pp)},
					Expected:   fmt.Sprintf("tag %d (NAD configuration)", *p.DeclaredVLAN),
					NextAction: "The port was configured differently than the NAD declares; restart the VM to re-create the attachment and check OVS CNI logs.",
				})
			}
			if len(p.DeclaredTrunks) > 0 && !trunksEqual(p.DeclaredTrunks, pp.Trunks) {
				out = append(out, Finding{
					ID: IDVLANMismatch, Severity: SeverityError, Category: Confirmed, Resource: res(),
					Summary:    fmt.Sprintf("OVS port %s of network %q has trunks %v", pp.Name, p.Network, pp.Trunks),
					Expected:   fmt.Sprintf("trunks %s (NAD configuration)", rangesString(p.DeclaredTrunks)),
					Evidence:   []string{fmt.Sprintf("port vlan_mode %s", displayOr(pp.VLANMode, "<unset>"))},
					NextAction: "Restart the VM to re-create the attachment and check OVS CNI logs.",
				})
			}
		}

		if p.Kind == hostpath.KindLinuxBridge && p.Device != nil && p.Device.VLANFiltering != nil && !*p.Device.VLANFiltering &&
			(p.DeclaredVLAN != nil && *p.DeclaredVLAN > 0 || len(p.DeclaredTrunks) > 0) {
			out = append(out, Finding{
				ID: IDVLANNotEnforced, Severity: SeverityWarning, Category: Confirmed, Resource: res(),
				Summary: fmt.Sprintf("Bridge %q has VLAN filtering disabled, so the VLAN configuration of network %q has no effect", p.Device.Name, p.Network),
				Evidence: []string{
					"NAD config declares VLANs: " + nadSummary(in.Topology, p.Network),
					"bridge vlan_filtering: 0",
					"the bridge CNI plugin enables VLAN filtering only when it creates the bridge",
				},
				Expected:   "vlan_filtering enabled (implied by the NAD's VLAN configuration)",
				NextAction: "Enable VLAN filtering on the bridge (ip link set " + p.Device.Name + " type bridge vlan_filtering 1) through your node network configuration.",
			})
		}

		if !ok || len(p.Uplinks) == 0 || (p.Kind != hostpath.KindLinuxBridge && p.Kind != hostpath.KindOVS) {
			continue
		}
		if p.Kind == hostpath.KindLinuxBridge && (p.Device == nil || p.Device.VLANFiltering == nil || !*p.Device.VLANFiltering) {
			continue // without filtering the bridge does not restrict VLANs per port
		}
		var carrying, restricted []string
		unknown := false
		for _, u := range p.Uplinks {
			switch {
			case len(u.VLANs) == 0 && p.Kind == hostpath.KindOVS:
				carrying = append(carrying, u.Name) // no tag or trunks: OVS port carries all VLANs
			case len(u.VLANs) == 0:
				unknown = true
			case vlansContain(u.VLANs, vlan):
				carrying = append(carrying, u.Name)
			default:
				restricted = append(restricted, fmt.Sprintf("%s allows %s", u.Name, displayOr(vlanList(u.VLANs), u.VLANInfo)))
			}
		}
		if len(carrying) == 0 && len(restricted) > 0 && !unknown {
			out = append(out, Finding{
				ID: IDVLANMismatch, Severity: SeverityError, Category: Confirmed, Resource: res(),
				Summary:    fmt.Sprintf("No uplink of %q carries VLAN %d used by network %q", p.DeclaredDevice, vlan, p.Network),
				Evidence:   restricted,
				Expected:   fmt.Sprintf("an uplink allowing VLAN %d (%s)", vlan, source),
				NextAction: "Add the VLAN to the uplink's allowed VLANs on the node, and verify the switch port trunk carries it too.",
			})
		}
	}
	return out
}

func nadSummary(t *topology.Topology, network string) string {
	if n := networkByName(t, network); n != nil && n.NAD != nil && n.NAD.Config != nil {
		return n.NAD.Config.Summary()
	}
	return "<unavailable>"
}

func portVLANString(pp *hostpath.OVSPortView) string {
	s := fmt.Sprintf("port vlan_mode %s, tag %s", displayOr(pp.VLANMode, "<unset>"), intOr(pp.Tag, "none"))
	if len(pp.Trunks) > 0 {
		s += fmt.Sprintf(", trunks %v", pp.Trunks)
	}
	return s
}

func intOr(v *int, fallback string) string {
	if v == nil {
		return fallback
	}
	return fmt.Sprint(*v)
}

func vlansContain(vs []host.PortVLANs, vlan int) bool {
	for _, v := range vs {
		if vlan >= v.Min && vlan <= v.Max {
			return true
		}
	}
	return false
}

func vlanList(vs []host.PortVLANs) string {
	var parts []string
	for _, v := range vs {
		if v.Min == v.Max {
			parts = append(parts, fmt.Sprint(v.Min))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", v.Min, v.Max))
		}
	}
	return strings.Join(parts, ",")
}

func trunksEqual(declared []multus.VLANRange, actual []int) bool {
	want := map[int]bool{}
	for _, r := range declared {
		for v := r.Min; v <= r.Max; v++ {
			want[v] = true
		}
	}
	if len(want) != len(actual) {
		return false
	}
	for _, v := range actual {
		if !want[v] {
			return false
		}
	}
	return true
}

func rangesString(rs []multus.VLANRange) string {
	var parts []string
	for _, r := range rs {
		if r.Min == r.Max {
			parts = append(parts, fmt.Sprint(r.Min))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.Min, r.Max))
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// requiredMTU returns the MTU the attachment needs and its source.
func requiredMTU(in *Input, p hostpath.NetworkPath) (int, string, bool) {
	if p.DeclaredMTU != nil && *p.DeclaredMTU > 0 {
		return *p.DeclaredMTU, "NAD configuration", true
	}
	if e, ok := in.Expect.Network(p.Network); ok && e.MTU != nil {
		return *e.MTU, "expectations file", true
	}
	return 0, "", false
}

func checkMTU(in *Input) []Finding {
	var out []Finding
	for _, n := range in.Topology.Networks {
		e, ok := in.Expect.Network(n.Name)
		if !ok || e.MTU == nil || n.NAD == nil || n.NAD.Config == nil || n.NAD.Config.Primary() == nil {
			continue
		}
		if m := n.NAD.Config.Primary().MTU; m != nil && *m != *e.MTU {
			out = append(out, Finding{
				ID: IDMTUMismatch, Severity: SeverityError, Category: Confirmed, Resource: nadResource(n.NAD.Ref),
				Summary:    fmt.Sprintf("Network %q: NAD %s sets MTU %d, but MTU %d is expected", n.Name, n.NAD.Ref, *m, *e.MTU),
				Expected:   fmt.Sprintf("MTU %d (expectations file)", *e.MTU),
				Evidence:   []string{"NAD config: " + n.NAD.Config.Summary()},
				NextAction: "Correct the NAD's \"mtu\" or the expectations file.",
			})
		}
	}

	for _, p := range hostPaths(in) {
		mtu, source, ok := requiredMTU(in, p)
		if !ok {
			continue
		}
		var low []string
		if d := p.Device; d != nil && d.MTU != nil && *d.MTU < mtu && p.Kind != hostpath.KindOVS {
			low = append(low, fmt.Sprintf("%s %s has MTU %d", d.Type, d.Name, *d.MTU))
		}
		if pp := p.PodPort; pp != nil && pp.MTU != nil && *pp.MTU < mtu {
			low = append(low, fmt.Sprintf("OVS pod port %s has MTU %d", pp.Name, *pp.MTU))
		}
		p.Walk(func(u *hostpath.Uplink) {
			if u.MTU != nil && *u.MTU < mtu {
				low = append(low, fmt.Sprintf("uplink %s has MTU %d", u.Name, *u.MTU))
			}
		})
		for _, b := range p.Bonds() {
			for _, m := range b.Members {
				if m.MTU != nil && *m.MTU < mtu {
					low = append(low, fmt.Sprintf("bond %s member %s has MTU %d", b.Name, m.Name, *m.MTU))
				}
			}
		}
		if len(low) == 0 {
			continue
		}
		out = append(out, Finding{
			ID: IDMTUMismatch, Severity: SeverityError, Category: Confirmed, Resource: snapshotResource(in),
			Summary:    fmt.Sprintf("Host devices on the path of network %q have an MTU below %d", p.Network, mtu),
			Evidence:   low,
			Expected:   fmt.Sprintf("MTU >= %d on every device on the path (%s)", mtu, source),
			NextAction: "Raise the MTU of the listed devices on the node (and on the switch ports), or lower the NAD MTU.",
		})
	}
	return out
}

func checkBonds(in *Input) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, p := range hostPaths(in) {
		for _, b := range p.Bonds() {
			if seen[b.Name] {
				continue
			}
			seen[b.Name] = true
			out = append(out, bondFindings(in, p.Network, b)...)
		}
	}
	return out
}

func bondFindings(in *Input, network string, b *hostpath.Bond) []Finding {
	var out []Finding
	res := snapshotResource(in)
	var detached, attached []string
	members := map[string]bool{}
	for _, m := range b.Members {
		members[m.Name] = true
		if m.Detached {
			detached = append(detached, fmt.Sprintf("%s: %s", m.Name, m.Reason))
		} else {
			attached = append(attached, m.Name)
		}
	}
	if len(detached) > 0 {
		sev, summary := SeverityWarning, fmt.Sprintf("Bond %q on the path of network %q has detached members", b.Name, network)
		if len(attached) == 0 {
			sev, summary = SeverityError, fmt.Sprintf("All members of bond %q on the path of network %q are detached", b.Name, network)
		}
		ev := append([]string{}, detached...)
		if !b.RuntimeObserved {
			ev = append(ev, "OVS bond runtime state was unavailable; member state is derived from interface link state")
		}
		out = append(out, Finding{
			ID: IDBondMemberDetached, Severity: sev, Category: Confirmed, Resource: res,
			Summary: summary, Evidence: ev, Expected: "all members attached",
			NextAction: "Check the member links and the switch-side LAG configuration (ovs-appctl bond/show or /proc/net/bonding/" + b.Name + ").",
		})
	}

	exp, hasExp := in.Expect.Bond(b.Name)
	if hasExp {
		var missing []string
		for _, want := range exp.Members {
			if !members[want] {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			out = append(out, Finding{
				ID: IDBondMemberDetached, Severity: SeverityWarning, Category: Confirmed, Resource: res,
				Summary:    fmt.Sprintf("Bond %q is missing expected members", b.Name),
				Evidence:   []string{"current members: " + strings.Join(sortedKeys(members), ", ")},
				Expected:   "members " + strings.Join(exp.Members, ", ") + " (expectations file)",
				NextAction: "Add the missing interfaces to the bond in the node network configuration.",
			})
		}
	}

	partners := map[string][]string{}
	for _, m := range b.Members {
		if id := normalizeSysID(m.PartnerID); id != "" {
			partners[id] = append(partners[id], m.Name)
		}
	}
	if len(partners) > 1 {
		var ev []string
		for _, id := range sortedKeys(partnerSet(partners)) {
			ev = append(ev, fmt.Sprintf("partner %s via %s", id, strings.Join(partners[id], ", ")))
		}
		out = append(out, Finding{
			ID: IDLACPPartnerMismatch, Severity: SeverityError, Category: Confirmed, Resource: res,
			Summary:    fmt.Sprintf("Members of bond %q negotiated LACP with different partners", b.Name),
			Evidence:   ev,
			Expected:   "one LACP partner system for all members (consistency between members)",
			NextAction: "Verify that all member ports are in the same LAG (or MLAG pair presenting one system ID) on the switch.",
		})
	}
	if hasExp && exp.PartnerSystemID != "" {
		want := normalizeSysID(exp.PartnerSystemID)
		for id, names := range partners {
			if id != want {
				out = append(out, Finding{
					ID: IDLACPPartnerMismatch, Severity: SeverityError, Category: Confirmed, Resource: res,
					Summary:    fmt.Sprintf("Bond %q members report LACP partner %s", b.Name, id),
					Evidence:   []string{"members: " + strings.Join(names, ", ")},
					Expected:   fmt.Sprintf("partner system %s (expectations file)", want),
					NextAction: "Check which switch the members are cabled to and its LACP system ID.",
				})
			}
		}
	}
	return out
}

// normalizeSysID lower-cases a partner system ID; an all-zero ID (no LACPDU
// received) is treated as absent.
func normalizeSysID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if strings.Trim(id, "0:") == "" {
		return ""
	}
	return id
}

func partnerSet(m map[string][]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkTopologyGaps(in *Input) []Finding {
	g := graph.Build(in.Topology, in.Host)
	if len(g.Gaps) == 0 {
		return nil
	}
	return []Finding{{
		ID: IDTopologyIncomplete, Severity: SeverityInfo, Category: Unavailable,
		Resource:   vmiResource(in.Topology),
		Summary:    fmt.Sprintf("%d part(s) of the VM's network path could not be observed", len(g.Gaps)),
		Evidence:   g.Gaps,
		NextAction: "See kubectl netdoctor topology for the observed and inferred relationships.",
	}}
}
