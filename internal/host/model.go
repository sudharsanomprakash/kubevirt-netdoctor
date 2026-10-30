// Package host defines the host network snapshot used for node-level
// diagnostics, the parsers that build it from standard Linux and Open vSwitch
// tools, and the opt-in collector that runs those tools on a node.
//
// A snapshot describes configuration and state as reported by the host at
// collection time. It does not prove that packets are forwarded.
package host

import "strings"

// SchemaVersion versions the snapshot file format.
const SchemaVersion = "netdoctor.host/v1alpha1"

// Snapshot is the host network state of one node.
type Snapshot struct {
	SchemaVersion string `json:"schemaVersion"`
	// NodeName is the Kubernetes node name the snapshot claims to describe.
	NodeName    string `json:"nodeName"`
	Hostname    string `json:"hostname,omitempty"`
	CollectedAt string `json:"collectedAt,omitempty"`
	// Sources records what each collector did, so missing data is never
	// mistaken for absent configuration.
	Sources     []SourceStatus   `json:"sources"`
	Interfaces  []Interface      `json:"interfaces,omitempty"`
	Routes      []Route          `json:"routes,omitempty"`
	BridgeVLANs []BridgePortVLAN `json:"bridgeVLANs,omitempty"`
	Namespaces  []string         `json:"namespaces,omitempty"`
	// OVS is nil when OVS state was not collected or OVS is not installed;
	// the corresponding SourceStatus says which.
	OVS *OVS `json:"ovs,omitempty"`
}

// Source names.
const (
	SourceLinks       = "links"
	SourceAddresses   = "addresses"
	SourceRoutes      = "routes"
	SourceBridgeVLANs = "bridge-vlans"
	SourceNamespaces  = "namespaces"
	SourceBonding     = "bonding"
	SourceOVSDB       = "ovsdb"
	SourceOVSBonds    = "ovs-bonds"
	SourceOVSLACP     = "ovs-lacp"
	SourceOVSFlows    = "ovs-flows"
)

// Source states.
const (
	StatusOK = "OK"
	// StatusNotInstalled means the tool is absent (for example OVS on a node
	// that does not use it). This is not an error.
	StatusNotInstalled = "NotInstalled"
	// StatusFailed means the tool exists but could not be run successfully,
	// typically because of missing privileges.
	StatusFailed  = "Failed"
	StatusSkipped = "Skipped"
)

// SourceStatus describes the outcome of one collector.
type SourceStatus struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Command []string `json:"command,omitempty"`
	Message string   `json:"message,omitempty"`
}

// Source returns the status of the named source and whether it was recorded.
func (s *Snapshot) Source(name string) (SourceStatus, bool) {
	for _, src := range s.Sources {
		if src.Name == name {
			return src, true
		}
	}
	return SourceStatus{}, false
}

// SourceOK reports whether the named source was collected successfully.
func (s *Snapshot) SourceOK(name string) bool {
	src, ok := s.Source(name)
	return ok && src.Status == StatusOK
}

// Interface is a Linux network interface.
type Interface struct {
	Name  string `json:"name"`
	Index int    `json:"index,omitempty"`
	// Kind is the link type from rtnetlink (bridge, bond, vlan, veth,
	// openvswitch, ...); empty for physical NICs.
	Kind string `json:"kind,omitempty"`
	MTU  int    `json:"mtu,omitempty"`
	MAC  string `json:"mac,omitempty"`
	// OperState is the kernel operational state (UP, DOWN, UNKNOWN, ...).
	OperState string `json:"operState,omitempty"`
	AdminUp   bool   `json:"adminUp"`
	// Master is the bridge or bond this interface is enslaved to.
	Master string `json:"master,omitempty"`
	// Parent is the lower device of a VLAN or macvlan interface.
	Parent string `json:"parent,omitempty"`
	// PeerNetnsID is set for veth interfaces whose peer is in another
	// network namespace.
	PeerNetnsID *int     `json:"peerNetnsId,omitempty"`
	Addresses   []string `json:"addresses,omitempty"`

	VLAN       *VLANInfo       `json:"vlan,omitempty"`
	Bridge     *BridgeInfo     `json:"bridge,omitempty"`
	Bond       *BondInfo       `json:"bond,omitempty"`
	BondMember *BondMemberInfo `json:"bondMember,omitempty"`
}

// IsDown reports whether the interface is administratively or operationally
// down. UNKNOWN is common for virtual devices and is not treated as down.
func (i *Interface) IsDown() bool {
	return !i.AdminUp || i.OperState == "DOWN" || i.OperState == "LOWERLAYERDOWN"
}

// VLANInfo describes an 802.1Q/802.1ad sub-interface.
type VLANInfo struct {
	ID       int    `json:"id"`
	Protocol string `json:"protocol,omitempty"`
}

// BridgeInfo describes a Linux bridge.
type BridgeInfo struct {
	VLANFiltering bool `json:"vlanFiltering"`
}

// BondInfo describes a Linux bond master.
type BondInfo struct {
	Mode string `json:"mode,omitempty"`
	// ActiveAggregator is the 802.3ad active aggregator ID (0 if not LACP).
	ActiveAggregator int    `json:"activeAggregator,omitempty"`
	PartnerMAC       string `json:"partnerMac,omitempty"`
}

// BondMemberInfo describes a Linux bond member.
type BondMemberInfo struct {
	MIIStatus    string `json:"miiStatus,omitempty"`
	State        string `json:"state,omitempty"`
	AggregatorID int    `json:"aggregatorId,omitempty"`
	// PartnerMAC is the LACP partner system MAC this member negotiated with,
	// from /proc/net/bonding.
	PartnerMAC string `json:"partnerMac,omitempty"`
}

// BridgePortVLAN is the VLAN membership of one bridge port (bridge vlan show).
type BridgePortVLAN struct {
	Port  string      `json:"port"`
	VLANs []PortVLANs `json:"vlans"`
}

// PortVLANs is a VLAN (or range) on a bridge port.
type PortVLANs struct {
	Min      int  `json:"min"`
	Max      int  `json:"max"`
	PVID     bool `json:"pvid,omitempty"`
	Untagged bool `json:"untagged,omitempty"`
}

// Route is an entry of a host routing table.
type Route struct {
	Table string `json:"table"`
	// Dst is a prefix; "default" is normalized to 0.0.0.0/0 or ::/0.
	Dst      string `json:"dst"`
	Gateway  string `json:"gateway,omitempty"`
	Dev      string `json:"dev,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Metric   int    `json:"metric,omitempty"`
	// Type is the route type; empty means unicast.
	Type string `json:"type,omitempty"`
}

// OVS is the Open vSwitch state of the host.
type OVS struct {
	Bridges    []OVSBridge    `json:"bridges"`
	Ports      []OVSPort      `json:"ports"`
	Interfaces []OVSInterface `json:"interfaces"`
	Bonds      []OVSBond      `json:"bonds,omitempty"`
}

// OVSBridge is an OVS bridge.
type OVSBridge struct {
	Name         string   `json:"name"`
	FailMode     string   `json:"failMode,omitempty"`
	DatapathType string   `json:"datapathType,omitempty"`
	Ports        []string `json:"ports"`
	// Flows is nil when OpenFlow tables were not collected.
	Flows *FlowSummary `json:"flows,omitempty"`
}

// FlowSummary summarizes an OpenFlow table dump. It is evidence of
// configuration only; it does not show that traffic matches the flows.
type FlowSummary struct {
	Count int `json:"count"`
	// HasNormal is set when at least one flow uses the NORMAL action
	// (traditional L2 switching).
	HasNormal bool `json:"hasNormal"`
}

// OVSPort is an OVS port.
type OVSPort struct {
	Name        string            `json:"name"`
	Bridge      string            `json:"bridge,omitempty"`
	Interfaces  []string          `json:"interfaces"`
	Tag         *int              `json:"tag,omitempty"`
	Trunks      []int             `json:"trunks,omitempty"`
	VLANMode    string            `json:"vlanMode,omitempty"`
	BondMode    string            `json:"bondMode,omitempty"`
	LACP        string            `json:"lacp,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

// OVSInterface is an OVS interface.
type OVSInterface struct {
	Name        string            `json:"name"`
	Type        string            `json:"type,omitempty"`
	OFPort      int               `json:"ofport,omitempty"`
	AdminState  string            `json:"adminState,omitempty"`
	LinkState   string            `json:"linkState,omitempty"`
	MTU         *int              `json:"mtu,omitempty"`
	MAC         string            `json:"mac,omitempty"`
	Error       string            `json:"error,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

// OVSBond is the runtime state of an OVS bond (ovs-appctl bond/show and
// lacp/show).
type OVSBond struct {
	Name       string          `json:"name"`
	Mode       string          `json:"mode,omitempty"`
	LACPStatus string          `json:"lacpStatus,omitempty"`
	Members    []OVSBondMember `json:"members"`
}

// OVSBondMember is one member of an OVS bond.
type OVSBondMember struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// LACPState is the member's LACP state words, e.g. "current attached".
	LACPState    string `json:"lacpState,omitempty"`
	PartnerSysID string `json:"partnerSysId,omitempty"`
}

// OVS CNI identifies the ports it creates with these Port external_ids.
const (
	OVSCNIOwner         = "ovs-cni.network.kubevirt.io"
	ExternalIDOwner     = "owner"
	ExternalIDPodUID    = "contPodUid"
	ExternalIDContIface = "contIface"
	ExternalIDContNetns = "contNetns"
)

// NamesAgree reports whether a Kubernetes node name plausibly refers to the
// host with the given hostname: their first DNS labels must match, ignoring
// case ("worker-1.example.internal" agrees with "worker-1"). An unknown
// hostname is treated as agreeing.
func NamesAgree(nodeName, hostname string) bool {
	if hostname == "" {
		return true
	}
	first := func(s string) string {
		label, _, _ := strings.Cut(s, ".")
		return strings.ToLower(label)
	}
	return first(nodeName) == first(hostname)
}

// FindInterface returns the Linux interface with the given name.
func (s *Snapshot) FindInterface(name string) *Interface {
	for i := range s.Interfaces {
		if s.Interfaces[i].Name == name {
			return &s.Interfaces[i]
		}
	}
	return nil
}

// Members returns the interfaces enslaved to the given master.
func (s *Snapshot) Members(master string) []*Interface {
	var out []*Interface
	for i := range s.Interfaces {
		if s.Interfaces[i].Master == master {
			out = append(out, &s.Interfaces[i])
		}
	}
	return out
}

// PortVLANs returns the bridge VLAN membership of a port, if collected.
func (s *Snapshot) PortVLANs(port string) ([]PortVLANs, bool) {
	for _, p := range s.BridgeVLANs {
		if p.Port == port {
			return p.VLANs, true
		}
	}
	return nil, false
}

// FindBridge returns the OVS bridge with the given name.
func (o *OVS) FindBridge(name string) *OVSBridge {
	for i := range o.Bridges {
		if o.Bridges[i].Name == name {
			return &o.Bridges[i]
		}
	}
	return nil
}

// FindPort returns the OVS port with the given name.
func (o *OVS) FindPort(name string) *OVSPort {
	for i := range o.Ports {
		if o.Ports[i].Name == name {
			return &o.Ports[i]
		}
	}
	return nil
}

// FindInterface returns the OVS interface with the given name.
func (o *OVS) FindInterface(name string) *OVSInterface {
	for i := range o.Interfaces {
		if o.Interfaces[i].Name == name {
			return &o.Interfaces[i]
		}
	}
	return nil
}

// FindBond returns the runtime state of the OVS bond with the given name.
func (o *OVS) FindBond(name string) *OVSBond {
	for i := range o.Bonds {
		if o.Bonds[i].Name == name {
			return &o.Bonds[i]
		}
	}
	return nil
}
