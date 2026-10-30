// Package hostpath correlates the Kubernetes-level topology of a VM with a
// host network snapshot of the node it runs on: which host bridge, OVS port,
// uplinks and bond members carry each VM network.
//
// Only relationships that the snapshot shows, or that the NAD configuration
// declares, are recorded. Nothing here shows that traffic is forwarded.
package hostpath

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Data path kinds.
const (
	KindPodNetwork  = "PodNetwork"
	KindLinuxBridge = "LinuxBridge"
	KindOVS         = "OVS"
	KindMacvlan     = "Macvlan"
	// KindNotModeled covers CNI plugins whose host data path netdoctor does
	// not interpret (SR-IOV, host-device, OVN-Kubernetes overlays, ...).
	KindNotModeled = "NotModeled"
	// KindUnresolved means the NAD or its configuration is unavailable.
	KindUnresolved = "Unresolved"
)

// Presence states for host objects.
const (
	Found       = "Found"
	Missing     = "Missing"
	WrongBridge = "WrongBridge"
	// Unknown means the snapshot source needed to decide was unavailable.
	Unknown       = "Unknown"
	NotApplicable = "NotApplicable"
)

// Result is the host correlation for one VM.
type Result struct {
	SnapshotNode string `json:"snapshotNode"`
	VMINode      string `json:"vmiNode,omitempty"`
	// NodeMatch is false when the snapshot describes another node; no other
	// correlation is attempted in that case.
	NodeMatch bool          `json:"nodeMatch"`
	Paths     []NetworkPath `json:"paths,omitempty"`
}

// NetworkPath is the host side of one VMI network.
type NetworkPath struct {
	Network string `json:"network"`
	Kind    string `json:"kind"`
	// Declared* come from the NAD configuration.
	DeclaredDevice string             `json:"declaredDevice,omitempty"`
	DeclaredVLAN   *int               `json:"declaredVlan,omitempty"`
	DeclaredTrunks []multus.VLANRange `json:"declaredTrunks,omitempty"`
	DeclaredMTU    *int               `json:"declaredMtu,omitempty"`

	DeviceState string `json:"deviceState"`
	// DeviceReason explains a Missing or Unknown device state.
	DeviceReason string      `json:"deviceReason,omitempty"`
	Device       *DeviceView `json:"device,omitempty"`
	// PodPort is the OVS port created for this attachment (OVS only).
	PodPortState string       `json:"podPortState"`
	PodPort      *OVSPortView `json:"podPort,omitempty"`
	Uplinks      []Uplink     `json:"uplinks,omitempty"`
	// Notes record visibility limitations for this path.
	Notes []string `json:"notes,omitempty"`
}

// DeviceView is the host bridge or parent device of an attachment.
type DeviceView struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	OperState     string            `json:"operState,omitempty"`
	AdminUp       *bool             `json:"adminUp,omitempty"`
	MTU           *int              `json:"mtu,omitempty"`
	VLANFiltering *bool             `json:"vlanFiltering,omitempty"`
	FailMode      string            `json:"failMode,omitempty"`
	Flows         *host.FlowSummary `json:"flows,omitempty"`
}

// OVSPortView is an OVS port as relevant to VLAN checks.
type OVSPortView struct {
	Name     string `json:"name"`
	Bridge   string `json:"bridge"`
	Tag      *int   `json:"tag,omitempty"`
	Trunks   []int  `json:"trunks,omitempty"`
	VLANMode string `json:"vlanMode,omitempty"`
	MTU      *int   `json:"mtu,omitempty"`
	// MatchedBy explains how the port was attributed to the VM.
	MatchedBy string `json:"matchedBy,omitempty"`
}

// Uplink is a port of the host device that leads off the node (physical
// NIC, bond, VLAN sub-interface or OVS patch port), expanded to its lower
// devices.
type Uplink struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	OperState string `json:"operState,omitempty"`
	Down      bool   `json:"down,omitempty"`
	MTU       *int   `json:"mtu,omitempty"`
	// VLANs the bridge port allows (Linux bridge VLAN filtering), or the OVS
	// port tag/trunks. Nil means unrestricted or not collected; see VLANInfo.
	VLANs    []host.PortVLANs `json:"vlans,omitempty"`
	VLANInfo string           `json:"vlanInfo,omitempty"`
	// Lower lists devices under this uplink: a VLAN interface's parent, and
	// so on down to physical NICs.
	Lower []Uplink `json:"lower,omitempty"`
	Bond  *Bond    `json:"bond,omitempty"`
}

// Bond is a Linux or OVS bond on the path.
type Bond struct {
	Name    string       `json:"name"`
	Type    string       `json:"type"` // "linux" or "ovs"
	Mode    string       `json:"mode,omitempty"`
	Members []BondMember `json:"members"`
	// RuntimeObserved is false for OVS bonds whose bond/show output was
	// unavailable; member state then derives from interface link state.
	RuntimeObserved bool `json:"runtimeObserved"`
}

// BondMember is one bond member.
type BondMember struct {
	Name     string `json:"name"`
	Detached bool   `json:"detached"`
	Reason   string `json:"reason,omitempty"`
	// PartnerID is the LACP partner system ID the member reports, if any.
	PartnerID string `json:"partnerId,omitempty"`
	MTU       *int   `json:"mtu,omitempty"`
}

// Correlate builds the host correlation. It returns nil when no snapshot is
// given.
func Correlate(t *topology.Topology, snap *host.Snapshot) *Result {
	if snap == nil {
		return nil
	}
	res := &Result{SnapshotNode: snap.NodeName, VMINode: t.NodeName}
	res.NodeMatch = t.NodeName != "" && snap.NodeName == t.NodeName
	if !res.NodeMatch {
		return res
	}
	var podUID string
	if t.Launcher != nil && t.Launcher.Selected != nil {
		podUID = t.Launcher.Selected.UID
	}
	for _, n := range t.Networks {
		res.Paths = append(res.Paths, correlateNetwork(n, snap, podUID))
	}
	return res
}

func correlateNetwork(n topology.NetworkPath, snap *host.Snapshot, podUID string) NetworkPath {
	p := NetworkPath{Network: n.Name, DeviceState: NotApplicable, PodPortState: NotApplicable}
	if n.Source == topology.SourcePod {
		p.Kind = KindPodNetwork
		p.Notes = append(p.Notes, "the cluster network data path is CNI-specific; netdoctor checks host routes for it with diagnose --destination")
		return p
	}
	if n.NAD == nil || n.NAD.Config == nil {
		p.Kind = KindUnresolved
		p.Notes = append(p.Notes, "NetworkAttachmentDefinition configuration is unavailable")
		return p
	}
	prim := n.NAD.Config.Primary()
	if prim == nil {
		p.Kind = KindNotModeled
		return p
	}
	p.DeclaredVLAN, p.DeclaredTrunks, p.DeclaredMTU = prim.VLAN, prim.TrunkVLANs, prim.MTU

	switch prim.Type {
	case "bridge", "cnv-bridge":
		p.Kind = KindLinuxBridge
		p.DeclaredDevice = prim.Bridge
		if p.DeclaredDevice == "" {
			p.DeclaredDevice = "cni0" // the bridge plugin's default bridge name
			p.Notes = append(p.Notes, `NAD sets no "bridge"; the bridge plugin default "cni0" is assumed`)
		}
		correlateLinuxBridge(&p, snap)
		p.Notes = append(p.Notes, "the host veth of a Linux bridge attachment cannot be identified without entering the pod network namespace")
	case "ovs":
		p.Kind = KindOVS
		p.DeclaredDevice = prim.Bridge
		correlateOVS(&p, n, snap, podUID)
	case "macvlan", "ipvlan":
		p.Kind = KindMacvlan
		p.DeclaredDevice = prim.Master
		if p.DeclaredDevice == "" {
			p.DeviceState = Unknown
			p.DeviceReason = `NAD sets no "master"; the plugin uses the default-route interface, which is not resolved`
			return p
		}
		correlateMacvlan(&p, snap)
	default:
		p.Kind = KindNotModeled
		p.Notes = append(p.Notes, fmt.Sprintf("host data path of CNI plugin %q is not modeled", prim.Type))
	}
	return p
}

func linuxDevice(i *host.Interface, typ string) *DeviceView {
	up, mtu := i.AdminUp, i.MTU
	d := &DeviceView{Name: i.Name, Type: typ, OperState: i.OperState, AdminUp: &up, MTU: &mtu}
	if i.Bridge != nil {
		f := i.Bridge.VLANFiltering
		d.VLANFiltering = &f
	}
	return d
}

func correlateLinuxBridge(p *NetworkPath, snap *host.Snapshot) {
	if !snap.SourceOK(host.SourceLinks) {
		p.DeviceState, p.DeviceReason = Unknown, "host links were not collected"
		return
	}
	br := snap.FindInterface(p.DeclaredDevice)
	if br == nil {
		p.DeviceState, p.DeviceReason = Missing, "no interface with this name in the host snapshot"
		return
	}
	if br.Kind != "bridge" {
		p.DeviceState = Missing
		p.DeviceReason = fmt.Sprintf("%s exists but is a %q device, not a Linux bridge", br.Name, kindName(br.Kind))
		return
	}
	p.DeviceState, p.Device = Found, linuxDevice(br, "linux-bridge")
	for _, m := range snap.Members(br.Name) {
		if isPodSide(m) {
			continue
		}
		u := linuxUplink(m, snap, 0)
		if vlans, ok := snap.PortVLANs(m.Name); ok {
			u.VLANs = vlans
		} else if snap.SourceOK(host.SourceBridgeVLANs) {
			u.VLANInfo = "no bridge VLAN entries"
		} else {
			u.VLANInfo = "bridge VLAN table not collected"
		}
		p.Uplinks = append(p.Uplinks, u)
	}
}

func correlateMacvlan(p *NetworkPath, snap *host.Snapshot) {
	if !snap.SourceOK(host.SourceLinks) {
		p.DeviceState, p.DeviceReason = Unknown, "host links were not collected"
		return
	}
	master := snap.FindInterface(p.DeclaredDevice)
	if master == nil {
		p.DeviceState, p.DeviceReason = Missing, "no interface with this name in the host snapshot"
		return
	}
	p.DeviceState, p.Device = Found, linuxDevice(master, kindName(master.Kind))
	p.Uplinks = []Uplink{linuxUplink(master, snap, 0)}
}

// isPodSide reports whether a bridge member is a pod/VM-facing device rather
// than an uplink.
func isPodSide(i *host.Interface) bool {
	return i.Kind == "veth" || i.Kind == "tun" || i.PeerNetnsID != nil
}

func kindName(kind string) string {
	if kind == "" {
		return "device"
	}
	return kind
}

// linuxUplink describes a Linux uplink and its lower devices.
func linuxUplink(i *host.Interface, snap *host.Snapshot, depth int) Uplink {
	mtu := i.MTU
	u := Uplink{Name: i.Name, Type: kindName(i.Kind), OperState: i.OperState, Down: i.IsDown(), MTU: &mtu}
	if depth > 4 {
		return u // defensive bound against malformed snapshots
	}
	if i.VLAN != nil && i.Parent != "" {
		u.Type = fmt.Sprintf("vlan %d", i.VLAN.ID)
		if parent := snap.FindInterface(i.Parent); parent != nil {
			u.Lower = append(u.Lower, linuxUplink(parent, snap, depth+1))
		}
	}
	if i.Bond != nil {
		u.Bond = linuxBond(i, snap)
	}
	return u
}

func linuxBond(b *host.Interface, snap *host.Snapshot) *Bond {
	bond := &Bond{Name: b.Name, Type: "linux", Mode: b.Bond.Mode, RuntimeObserved: true}
	for _, m := range snap.Members(b.Name) {
		mtu := m.MTU
		bm := BondMember{Name: m.Name, MTU: &mtu}
		if m.BondMember != nil {
			bm.PartnerID = m.BondMember.PartnerMAC
			switch {
			case !strings.EqualFold(m.BondMember.MIIStatus, "up"):
				bm.Detached, bm.Reason = true, "MII status "+m.BondMember.MIIStatus
			case b.Bond.ActiveAggregator > 0 && m.BondMember.AggregatorID > 0 && m.BondMember.AggregatorID != b.Bond.ActiveAggregator:
				bm.Detached = true
				bm.Reason = fmt.Sprintf("in aggregator %d, active aggregator is %d", m.BondMember.AggregatorID, b.Bond.ActiveAggregator)
			}
		}
		bond.Members = append(bond.Members, bm)
	}
	return bond
}

func correlateOVS(p *NetworkPath, n topology.NetworkPath, snap *host.Snapshot, podUID string) {
	src, recorded := snap.Source(host.SourceOVSDB)
	switch {
	case recorded && src.Status == host.StatusNotInstalled:
		p.DeviceState, p.DeviceReason = Missing, "Open vSwitch tools are not installed on the node"
		return
	case snap.OVS == nil:
		p.DeviceState, p.PodPortState = Unknown, Unknown
		p.DeviceReason = "OVS state was not collected"
		return
	}
	ovs := snap.OVS
	if p.DeclaredDevice == "" {
		p.DeviceState, p.DeviceReason = Unknown, `NAD sets no "bridge"`
	} else if br := ovs.FindBridge(p.DeclaredDevice); br == nil {
		p.DeviceState, p.DeviceReason = Missing, "no OVS bridge with this name in OVSDB"
	} else {
		p.DeviceState = Found
		p.Device = &DeviceView{Name: br.Name, Type: "ovs-bridge", FailMode: br.FailMode, Flows: br.Flows}
		if iface := ovs.FindInterface(br.Name); iface != nil {
			p.Device.MTU = iface.MTU
			p.Device.OperState = iface.LinkState
		}
		for _, name := range br.Ports {
			port := ovs.FindPort(name)
			if port == nil || port.Name == br.Name || port.ExternalIDs[host.ExternalIDOwner] == host.OVSCNIOwner {
				continue
			}
			p.Uplinks = append(p.Uplinks, ovsUplink(port, snap))
		}
	}
	correlateOVSPodPort(p, n, ovs, podUID)
}

func ovsUplink(port *host.OVSPort, snap *host.Snapshot) Uplink {
	u := Uplink{Name: port.Name, Type: "ovs-port"}
	switch {
	case port.Tag != nil:
		u.VLANs = []host.PortVLANs{{Min: *port.Tag, Max: *port.Tag, PVID: true, Untagged: true}}
		u.VLANInfo = fmt.Sprintf("access tag %d", *port.Tag)
	case len(port.Trunks) > 0:
		var ids []string
		for _, v := range port.Trunks {
			u.VLANs = append(u.VLANs, host.PortVLANs{Min: v, Max: v})
			ids = append(ids, fmt.Sprint(v))
		}
		u.VLANInfo = "trunk " + strings.Join(ids, ",")
	default:
		u.VLANInfo = "no tag or trunks (all VLANs)"
	}
	if len(port.Interfaces) > 1 || port.BondMode != "" {
		u.Type = "ovs-bond"
		u.Bond = ovsBond(port, snap)
		return u
	}
	if len(port.Interfaces) == 1 {
		if iface := snap.OVS.FindInterface(port.Interfaces[0]); iface != nil {
			u.MTU, u.OperState = iface.MTU, iface.LinkState
			u.Down = iface.LinkState == "down" || iface.AdminState == "down"
			if iface.Type != "" {
				u.Type = "ovs-" + iface.Type
			}
		}
		if li := snap.FindInterface(port.Interfaces[0]); li != nil && li.Bond != nil {
			u.Bond = linuxBond(li, snap) // Linux bond attached to OVS as a single port
		}
	}
	return u
}

func ovsBond(port *host.OVSPort, snap *host.Snapshot) *Bond {
	bond := &Bond{Name: port.Name, Type: "ovs", Mode: port.BondMode}
	if rt := snap.OVS.FindBond(port.Name); rt != nil && snap.SourceOK(host.SourceOVSBonds) {
		bond.RuntimeObserved = true
		for _, m := range rt.Members {
			bm := BondMember{Name: m.Name, PartnerID: m.PartnerSysID}
			if iface := snap.OVS.FindInterface(m.Name); iface != nil {
				bm.MTU = iface.MTU
			}
			state := strings.Fields(m.LACPState)
			switch {
			case !m.Enabled:
				bm.Detached, bm.Reason = true, "member disabled"
			case contains(state, "detached"), contains(state, "defaulted"), contains(state, "expired"):
				bm.Detached, bm.Reason = true, "LACP state "+m.LACPState
			}
			bond.Members = append(bond.Members, bm)
		}
		return bond
	}
	for _, name := range port.Interfaces {
		bm := BondMember{Name: name}
		if iface := snap.OVS.FindInterface(name); iface != nil {
			bm.MTU = iface.MTU
			if iface.LinkState == "down" || iface.AdminState == "down" {
				bm.Detached, bm.Reason = true, "link state down"
			}
		}
		bond.Members = append(bond.Members, bm)
	}
	return bond
}

// correlateOVSPodPort finds the port OVS CNI created for this attachment.
// OVS CNI tags its ports with external_ids owner, contPodUid and contIface,
// so the match is exact when the pod interface name is known.
func correlateOVSPodPort(p *NetworkPath, n topology.NetworkPath, ovs *host.OVS, podUID string) {
	if podUID == "" {
		p.PodPortState = Unknown
		p.Notes = append(p.Notes, "no virt-launcher pod was selected, so its OVS port cannot be identified")
		return
	}
	contIface := ""
	if n.PodAttachment != nil {
		contIface = n.PodAttachment.Interface
	}
	var matches []*host.OVSPort
	for i := range ovs.Ports {
		port := &ovs.Ports[i]
		ids := port.ExternalIDs
		if ids[host.ExternalIDOwner] != host.OVSCNIOwner || ids[host.ExternalIDPodUID] != podUID {
			continue
		}
		if contIface != "" && ids[host.ExternalIDContIface] != contIface {
			continue
		}
		matches = append(matches, port)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	switch {
	case len(matches) == 0:
		p.PodPortState = Missing
		return
	case len(matches) > 1:
		p.PodPortState = Unknown
		p.Notes = append(p.Notes, fmt.Sprintf("%d OVS CNI ports belong to the launcher pod and the pod interface name is unknown", len(matches)))
		return
	}
	port := matches[0]
	view := &OVSPortView{
		Name: port.Name, Bridge: port.Bridge, Tag: port.Tag, Trunks: port.Trunks, VLANMode: port.VLANMode,
		MatchedBy: "external_ids owner+contPodUid",
	}
	if contIface != "" {
		view.MatchedBy += "+contIface"
	}
	if len(port.Interfaces) == 1 {
		if iface := ovs.FindInterface(port.Interfaces[0]); iface != nil {
			view.MTU = iface.MTU
		}
	}
	p.PodPort = view
	if p.DeclaredDevice != "" && port.Bridge != p.DeclaredDevice {
		p.PodPortState = WrongBridge
		return
	}
	p.PodPortState = Found
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Bonds returns every bond reachable from the path's uplinks.
func (p *NetworkPath) Bonds() []*Bond {
	var out []*Bond
	var walk func(us []Uplink)
	walk = func(us []Uplink) {
		for i := range us {
			if us[i].Bond != nil {
				out = append(out, us[i].Bond)
			}
			walk(us[i].Lower)
		}
	}
	walk(p.Uplinks)
	return out
}

// Walk calls fn for every uplink and lower device on the path.
func (p *NetworkPath) Walk(fn func(u *Uplink)) {
	var walk func(us []Uplink)
	walk = func(us []Uplink) {
		for i := range us {
			fn(&us[i])
			walk(us[i].Lower)
		}
	}
	walk(p.Uplinks)
}
