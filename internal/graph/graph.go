// Package graph renders a VM's correlated network topology as nodes and
// edges. Every edge states how the relationship was established:
//
//   - Observed: reported by the Kubernetes API or the host snapshot.
//   - Declared: stated by configuration (VMI spec, NAD config) but the
//     relationship itself is not reported at runtime.
//   - Inferred: derived from documented behavior or matching heuristics.
package graph

import (
	"fmt"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Provenance values.
const (
	Observed = "Observed"
	Declared = "Declared"
	Inferred = "Inferred"
)

// Node kinds.
const (
	KindVM             = "VirtualMachine"
	KindVMI            = "VirtualMachineInstance"
	KindNode           = "Node"
	KindPod            = "LauncherPod"
	KindNetwork        = "VMINetwork"
	KindGuestIface     = "GuestInterface"
	KindPodIface       = "PodInterface"
	KindNAD            = "NetworkAttachmentDefinition"
	KindHostBridge     = "HostBridge"
	KindOVSBridge      = "OVSBridge"
	KindOVSPort        = "OVSPort"
	KindHostIface      = "HostInterface"
	KindBond           = "Bond"
	KindBondMember     = "BondMember"
	KindClusterNetwork = "ClusterNetwork"
)

// Graph is the topology graph.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	// Gaps list layers or relationships that could not be observed.
	Gaps []string `json:"gaps"`
	// Absent lists layers whose source was readable but did not contain
	// the expected element (diagnose reports these as findings).
	Absent []string `json:"absent"`
}

// Node is a topology element.
type Node struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Edge is a relationship between two nodes.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`
	Provenance string `json:"provenance"`
	Evidence   string `json:"evidence"`
}

type builder struct {
	g     Graph
	nodes map[string]bool
}

func (b *builder) node(kind, name string, attrs map[string]string) string {
	id := kind + "/" + name
	if !b.nodes[id] {
		b.nodes[id] = true
		for k, v := range attrs {
			if v == "" {
				delete(attrs, k)
			}
		}
		if len(attrs) == 0 {
			attrs = nil
		}
		b.g.Nodes = append(b.g.Nodes, Node{ID: id, Kind: kind, Name: name, Attributes: attrs})
	}
	return id
}

func (b *builder) edge(from, to, relation, provenance, evidence string) {
	b.g.Edges = append(b.g.Edges, Edge{From: from, To: to, Relation: relation, Provenance: provenance, Evidence: evidence})
}

func (b *builder) gap(format string, args ...any) {
	b.g.Gaps = append(b.g.Gaps, fmt.Sprintf(format, args...))
}

func (b *builder) absent(format string, args ...any) {
	b.g.Absent = append(b.g.Absent, fmt.Sprintf(format, args...))
}

// Build constructs the graph. hp may be nil when no host snapshot was given.
func Build(t *topology.Topology, hp *hostpath.Result) *Graph {
	b := &builder{nodes: map[string]bool{}, g: Graph{Nodes: []Node{}, Edges: []Edge{}, Gaps: []string{}, Absent: []string{}}}
	ns := t.Target.Namespace

	var vmID, vmiID, podID, nodeID string
	if vm := t.VirtualMachine; vm != nil && vm.Lookup == kube.StateFound {
		vmID = b.node(KindVM, ns+"/"+vm.Name, map[string]string{"runStrategy": vm.RunStrategy, "status": vm.PrintableStatus})
	}
	inst := t.Instance
	vmiFound := inst != nil && inst.Lookup == kube.StateFound
	if vmiFound {
		vmiID = b.node(KindVMI, ns+"/"+inst.Name, map[string]string{"phase": inst.Phase, "guestAgent": inst.GuestAgent})
		if vmID != "" {
			b.edge(vmID, vmiID, "runs", Observed, "VMI owned by the VM")
		}
		if inst.NodeName != "" {
			nodeID = b.node(KindNode, inst.NodeName, nil)
			b.edge(vmiID, nodeID, "scheduled-on", Observed, "VMI status.nodeName")
		}
	} else {
		b.gap("no VirtualMachineInstance: networks are taken from the VM template and no runtime layer is observable")
	}

	if l := t.Launcher; l != nil && l.Selected != nil {
		podID = b.node(KindPod, ns+"/"+l.Selected.Name, map[string]string{"phase": l.Selected.Phase, "uid": l.Selected.UID})
		if vmiID != "" {
			b.edge(vmiID, podID, "hosted-by", Observed, l.SelectionReason)
		}
		if l.Selected.NodeName != "" {
			nid := b.node(KindNode, l.Selected.NodeName, nil)
			b.edge(podID, nid, "scheduled-on", Observed, "pod spec.nodeName")
		}
		if !l.NetworkStatusPresent {
			b.gap("the launcher pod has no Multus network-status annotation: pod interfaces are not observable")
		}
	} else if vmiFound {
		b.gap("no virt-launcher pod was selected: pod interfaces are not observable")
	}

	for _, n := range t.Networks {
		b.network(t, n, vmiID, podID)
	}
	for _, a := range t.UnmatchedPodAttachments {
		id := b.node(KindPodIface, podKey(t, a.Interface), map[string]string{"network": a.NetworkName, "mac": a.MAC, "ips": strings.Join(a.IPs, ",")})
		if podID != "" {
			b.edge(podID, id, "has-interface", Observed, "Multus network-status (not attributed to any VMI network)")
		}
	}

	switch {
	case hp == nil:
		b.gap("no host snapshot: host bridges, OVS ports, uplinks and bonds are not observed (see kubectl netdoctor host collect)")
	case !hp.NodeMatch:
		b.gap("the host snapshot describes node %q but the VMI runs on %q: host layers were not correlated", hp.SnapshotNode, hp.VMINode)
	default:
		for i := range hp.Paths {
			b.hostPath(t, &hp.Paths[i])
		}
	}
	return &b.g
}

func podKey(t *topology.Topology, iface string) string {
	pod := "unknown-pod"
	if t.Launcher != nil && t.Launcher.Selected != nil {
		pod = t.Launcher.Selected.Name
	}
	return t.Target.Namespace + "/" + pod + "/" + iface
}

func (b *builder) network(t *topology.Topology, n topology.NetworkPath, vmiID, podID string) {
	ns := t.Target.Namespace
	binding := ""
	if n.Interface != nil {
		binding = n.Interface.Binding
	}
	netID := b.node(KindNetwork, ns+"/"+t.Target.Name+"/"+n.Name, map[string]string{"source": n.Source, "binding": binding})
	if vmiID != "" {
		b.edge(vmiID, netID, "declares", Declared, "VMI spec.networks / spec.domain.devices.interfaces")
	}

	var guestID string
	if g := n.Guest; g != nil {
		guestID = b.node(KindGuestIface, ns+"/"+t.Target.Name+"/"+n.Name, map[string]string{
			"interfaceName": g.InterfaceName, "mac": g.MAC, "ips": strings.Join(g.IPs, ","), "infoSource": g.InfoSource,
		})
		b.edge(netID, guestID, "realized-as", Observed, "VMI status.interfaces")
	} else if vmiID != "" {
		b.absent("network %q: guest interface not reported in VMI status", n.Name)
	}

	var podIfaceID string
	if a := n.PodAttachment; a != nil {
		podIfaceID = b.node(KindPodIface, podKey(t, a.Interface), map[string]string{"network": a.NetworkName, "mac": a.MAC, "ips": strings.Join(a.IPs, ",")})
		if podID != "" {
			b.edge(podID, podIfaceID, "has-interface", Observed, "Multus network-status")
		}
		prov, ev := Observed, "matched by "+a.MatchedBy
		if a.MatchedBy == topology.MatchReferenceOrder {
			prov, ev = Inferred, "matched by NAD reference in order of occurrence; no interface name was available"
		}
		b.edge(netID, podIfaceID, "attached-via", prov, ev)
		if guestID != "" && binding != "" {
			b.edge(guestID, podIfaceID, "bound-to", Inferred, bindingSemantics(binding))
		}
	} else if podID != "" && t.Launcher != nil && t.Launcher.NetworkStatusPresent {
		b.absent("network %q: no Multus network-status entry was attributed to it", n.Name)
	}

	switch {
	case n.Source == topology.SourcePod:
		cn := "cluster-default"
		if n.PodAttachment != nil {
			cn = n.PodAttachment.NetworkName
		}
		cnID := b.node(KindClusterNetwork, cn, nil)
		from := netID
		if podIfaceID != "" {
			from = podIfaceID
		}
		b.edge(from, cnID, "member-of", Declared, "pod network (primary CNI)")
	case n.NAD != nil && n.NAD.RefError == "":
		attrs := map[string]string{"lookup": string(n.NAD.Lookup)}
		if n.NAD.Config != nil {
			attrs["cni"] = n.NAD.Config.Summary()
		}
		nadID := b.node(KindNAD, n.NAD.Ref.String(), attrs)
		b.edge(netID, nadID, "references", Declared, "spec.networks[].multus.networkName")
		if podIfaceID != "" {
			b.edge(podIfaceID, nadID, "created-from", Observed, "network-status entry names the NAD")
		}
		if n.NAD.ConfigNotInline {
			b.gap("NAD %s: CNI configuration is in a node-local file and was not observed", n.NAD.Ref)
		}
	}
}

func bindingSemantics(binding string) string {
	switch binding {
	case "masquerade":
		return "masquerade binding: guest traffic is NATed to the pod interface address"
	case "bridge":
		return "bridge binding: the pod interface is bridged to the guest at layer 2"
	case "sriov":
		return "SR-IOV binding: a VF is passed through to the guest; the pod interface is not in the data path"
	case "passt", "plugin:passt":
		return "passt binding: user-space translation between guest and pod interface"
	default:
		return binding + " binding (semantics not modeled)"
	}
}

func (b *builder) hostPath(t *topology.Topology, p *hostpath.NetworkPath) {
	ns := t.Target.Namespace
	netID := KindNetwork + "/" + ns + "/" + t.Target.Name + "/" + p.Network
	var nadID string
	for _, n := range t.Networks {
		if n.Name == p.Network && n.NAD != nil && n.NAD.RefError == "" {
			nadID = KindNAD + "/" + n.NAD.Ref.String()
		}
	}
	from := nadID
	if from == "" {
		from = netID
	}

	var devID string
	switch p.DeviceState {
	case hostpath.Found:
		kind := KindHostIface
		switch p.Kind {
		case hostpath.KindLinuxBridge:
			kind = KindHostBridge
		case hostpath.KindOVS:
			kind = KindOVSBridge
		}
		attrs := map[string]string{"type": p.Device.Type, "operState": p.Device.OperState, "failMode": p.Device.FailMode}
		if p.Device.MTU != nil {
			attrs["mtu"] = fmt.Sprint(*p.Device.MTU)
		}
		if p.Device.VLANFiltering != nil {
			attrs["vlanFiltering"] = fmt.Sprint(*p.Device.VLANFiltering)
		}
		devID = b.node(kind, p.Device.Name, attrs)
		b.edge(from, devID, "uses-host-device", Declared, fmt.Sprintf("NAD config names %q; present in the host snapshot", p.DeclaredDevice))
	case hostpath.Missing:
		b.absent("network %q: host device %q declared by the NAD: %s", p.Network, p.DeclaredDevice, p.DeviceReason)
	case hostpath.Unknown:
		b.gap("network %q: host device %q could not be checked: %s", p.Network, p.DeclaredDevice, p.DeviceReason)
	}

	if pp := p.PodPort; pp != nil {
		attrs := map[string]string{"bridge": pp.Bridge, "vlanMode": pp.VLANMode}
		if pp.Tag != nil {
			attrs["tag"] = fmt.Sprint(*pp.Tag)
		}
		portID := b.node(KindOVSPort, pp.Name, attrs)
		for _, n := range t.Networks {
			if n.Name == p.Network && n.PodAttachment != nil {
				b.edge(KindPodIface+"/"+podKey(t, n.PodAttachment.Interface), portID, "plugged-into", Observed, pp.MatchedBy)
			}
		}
		b.edge(portID, b.node(KindOVSBridge, pp.Bridge, nil), "port-of", Observed, "OVSDB Bridge.ports")
	} else if p.Kind == hostpath.KindOVS && p.PodPortState == hostpath.Missing {
		b.absent("network %q: no OVS CNI port of the launcher pod exists", p.Network)
	} else if p.Kind == hostpath.KindOVS && p.PodPortState == hostpath.Unknown {
		b.gap("network %q: the OVS CNI port of the launcher pod could not be identified", p.Network)
	}

	if devID == "" {
		return
	}
	for _, u := range p.Uplinks {
		b.uplink(devID, u)
	}
}

func (b *builder) uplink(parentID string, u hostpath.Uplink) {
	attrs := map[string]string{"type": u.Type, "operState": u.OperState, "vlans": u.VLANInfo}
	if u.MTU != nil {
		attrs["mtu"] = fmt.Sprint(*u.MTU)
	}
	if len(u.VLANs) > 0 {
		var parts []string
		for _, v := range u.VLANs {
			if v.Min == v.Max {
				parts = append(parts, fmt.Sprint(v.Min))
			} else {
				parts = append(parts, fmt.Sprintf("%d-%d", v.Min, v.Max))
			}
		}
		attrs["vlans"] = strings.Join(parts, ",")
	}
	id := b.node(KindHostIface, u.Name, attrs)
	b.edge(parentID, id, "uplink", Observed, "host snapshot port membership")
	for _, l := range u.Lower {
		b.uplink(id, l)
	}
	if bond := u.Bond; bond != nil {
		bondID := b.node(KindBond, bond.Name, map[string]string{"type": bond.Type, "mode": bond.Mode})
		if bondID != id {
			b.edge(id, bondID, "is-bond", Observed, "host snapshot")
		}
		for _, m := range bond.Members {
			state := "attached"
			if m.Detached {
				state = "detached: " + m.Reason
			}
			mID := b.node(KindBondMember, bond.Name+"/"+m.Name, map[string]string{"state": state, "partner": m.PartnerID})
			b.edge(bondID, mID, "member", Observed, "host snapshot bond membership")
		}
	}
}
