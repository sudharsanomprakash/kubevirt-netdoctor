// Package route analyzes how traffic from a VM to a destination address is
// expected to leave the VM and the node, based on declared configuration
// (NAD IPAM subnets and routes), KubeVirt binding semantics and, when a host
// snapshot is supplied, the node's routing table.
//
// This is configuration analysis. It sends no packets and cannot see the
// guest's own routing table.
package route

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Provenance values.
const (
	// Declared: stated by configuration (NAD IPAM).
	Declared = "Declared"
	// Inferred: derived from documented KubeVirt behavior, not observed.
	Inferred = "Inferred"
	// Observed: reported by the node (host snapshot).
	Observed = "Observed"
)

// Candidate kinds.
const (
	KindOnLink  = "OnLink"
	KindRoute   = "Route"
	KindDefault = "Default"
)

// Candidate is one way the VM could reach the destination.
type Candidate struct {
	Network    string `json:"network"`
	Kind       string `json:"kind"`
	Prefix     string `json:"prefix"`
	Gateway    string `json:"gateway,omitempty"`
	Provenance string `json:"provenance"`
	Reason     string `json:"reason"`
	// GatewayOffLink is set when a declared gateway is not inside any subnet
	// declared for the same network.
	GatewayOffLink bool `json:"gatewayOffLink,omitempty"`
	// GuestEvidence states whether the guest reports an address in the
	// network's declared subnets. NAD IPAM configures the pod interface; the
	// guest only uses that addressing if it is delivered to it.
	GuestEvidence string `json:"guestEvidence,omitempty"`
	prefixLen     int
}

// Analysis is the result for one destination.
type Analysis struct {
	Destination string      `json:"destination"`
	Candidates  []Candidate `json:"candidates"`
	// Selected is the longest-prefix candidate when it is unique.
	Selected  *Candidate `json:"selected,omitempty"`
	Ambiguous bool       `json:"ambiguous,omitempty"`
	Host      *HostRoute `json:"host,omitempty"`
	Notes     []string   `json:"notes"`
}

// HostRoute is the node routing table lookup.
type HostRoute struct {
	// Applicable is false when the selected network is bridged at layer 2
	// past the node's routing table (Linux bridge, OVS, macvlan).
	Applicable bool        `json:"applicable"`
	Reason     string      `json:"reason"`
	Route      *host.Route `json:"route,omitempty"`
	// Result is Routed, NoRoute, Rejected, EgressDown or Unknown.
	Result     string `json:"result"`
	EgressDown bool   `json:"egressDown,omitempty"`
}

// Host lookup results.
const (
	HostRouted     = "Routed"
	HostNoRoute    = "NoRoute"
	HostRejected   = "Rejected"
	HostEgressDown = "EgressDown"
	HostUnknown    = "Unknown"
)

// Analyze evaluates the destination against the topology and, optionally, a
// host snapshot of the VM's node (pass nil when unavailable or when it
// describes another node).
func Analyze(t *topology.Topology, dest netip.Addr, snap *host.Snapshot) *Analysis {
	dest = dest.Unmap()
	a := &Analysis{
		Destination: dest.String(),
		Notes: []string{
			"configuration analysis only: no packets were sent",
			"the guest's own routing table is not observable through the Kubernetes API",
			"NAD IPAM subnets and routes configure the pod interface; the guest uses them only if they are delivered to it (binding-dependent) or configured in the guest",
		},
	}
	for _, n := range t.Networks {
		a.Candidates = append(a.Candidates, candidatesFor(n, dest)...)
	}
	sort.SliceStable(a.Candidates, func(i, j int) bool { return a.Candidates[i].prefixLen > a.Candidates[j].prefixLen })

	if len(a.Candidates) > 0 {
		best := a.Candidates[0]
		for _, c := range a.Candidates[1:] {
			if c.prefixLen == best.prefixLen && c.Network != best.Network {
				a.Ambiguous = true
			}
		}
		if !a.Ambiguous {
			a.Selected = &best
		}
	}
	if snap != nil {
		a.Host = hostLookup(t, a.Selected, dest, snap)
	}
	return a
}

func candidatesFor(n topology.NetworkPath, dest netip.Addr) []Candidate {
	var out []Candidate
	if n.NAD != nil && n.NAD.Config != nil {
		if prim := n.NAD.Config.Primary(); prim != nil && prim.IPAMConfig != nil {
			ipam := prim.IPAMConfig
			subnets := ipam.ParsedSubnets()
			evidence := guestEvidence(n, subnets)
			for _, p := range subnets {
				if p.Contains(dest) {
					out = append(out, Candidate{
						Network: n.Name, Kind: KindOnLink, Prefix: p.String(), Provenance: Declared,
						Reason: fmt.Sprintf("destination is inside IPAM subnet %s of %s", p, n.NAD.Ref), prefixLen: p.Bits(),
						GuestEvidence: evidence,
					})
				}
			}
			defaultGW := firstGatewayFor(ipam.Gateways, dest)
			for _, r := range ipam.Routes {
				p, err := netip.ParsePrefix(r.Dst)
				if err != nil || !p.Contains(dest) {
					continue
				}
				gw := r.GW
				if gw == "" {
					gw = defaultGW
				}
				c := Candidate{
					Network: n.Name, Kind: KindRoute, Prefix: p.String(), Gateway: gw, Provenance: Declared,
					Reason: fmt.Sprintf("IPAM route %s of %s", p, n.NAD.Ref), prefixLen: p.Bits(),
					GuestEvidence: evidence,
				}
				if p.Bits() == 0 {
					c.Kind = KindDefault
				}
				if gw != "" {
					if g, err := netip.ParseAddr(gw); err == nil && !anyContains(subnets, g) && len(subnets) > 0 {
						c.GatewayOffLink = true
					}
				}
				out = append(out, c)
			}
		}
	}

	// KubeVirt hands the pod network's addressing, including the default
	// route, to the guest for masquerade, passt and bridge bindings on the
	// pod network (or a Multus default network).
	if n.Source == topology.SourcePod || n.MultusDefault {
		binding := ""
		if n.Interface != nil {
			binding = n.Interface.Binding
		}
		if binding == "masquerade" || binding == "bridge" || binding == "passt" || binding == "plugin:passt" {
			p := netip.PrefixFrom(netip.IPv4Unspecified(), 0)
			if dest.Is6() {
				p = netip.PrefixFrom(netip.IPv6Unspecified(), 0)
			}
			out = append(out, Candidate{
				Network: n.Name, Kind: KindDefault, Prefix: p.String(), Provenance: Inferred,
				Reason: fmt.Sprintf("KubeVirt provides the guest default route on the pod network for %s binding", binding),
			})
		}
	}
	return out
}

// guestEvidence reports whether the guest has an address inside the declared
// subnets of a network.
func guestEvidence(n topology.NetworkPath, subnets []netip.Prefix) string {
	if n.Guest == nil || len(n.Guest.IPs) == 0 {
		return "the guest reports no address on this network; whether it uses the declared addressing is unknown"
	}
	for _, ip := range n.Guest.IPs {
		if a, err := netip.ParseAddr(ip); err == nil && anyContains(subnets, a) {
			return fmt.Sprintf("the guest reports %s inside the declared subnets", ip)
		}
	}
	return fmt.Sprintf("the guest reports %s, outside the declared subnets", strings.Join(n.Guest.IPs, ", "))
}

func firstGatewayFor(gws []string, dest netip.Addr) string {
	for _, g := range gws {
		if a, err := netip.ParseAddr(g); err == nil && a.Is4() == dest.Is4() {
			return g
		}
	}
	return ""
}

func anyContains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func hostLookup(t *topology.Topology, sel *Candidate, dest netip.Addr, snap *host.Snapshot) *HostRoute {
	hr := &HostRoute{Result: HostUnknown}
	switch {
	case sel == nil:
		hr.Reason = "no egress network was selected for the VM"
		return hr
	case !isRoutedViaNode(t, sel.Network):
		hr.Reason = fmt.Sprintf("network %q is bridged at layer 2; the node routing table is not on its path", sel.Network)
		return hr
	case !snap.SourceOK(host.SourceRoutes):
		hr.Applicable = true
		hr.Reason = "the host routing table was not collected"
		return hr
	}
	hr.Applicable = true
	hr.Reason = "pod network traffic is routed by the node; the exact path is CNI-specific (main table only, policy routing not evaluated)"

	var best *host.Route
	bestLen := -1
	for i := range snap.Routes {
		r := &snap.Routes[i]
		if r.Table != "main" {
			continue
		}
		switch r.Type {
		case "", "blackhole", "unreachable", "prohibit":
		default:
			continue
		}
		p, err := netip.ParsePrefix(r.Dst)
		if err != nil || !p.Contains(dest) {
			continue
		}
		if p.Bits() > bestLen || (p.Bits() == bestLen && best != nil && r.Metric < best.Metric) {
			best, bestLen = r, p.Bits()
		}
	}
	switch {
	case best == nil:
		hr.Result = HostNoRoute
	case best.Type != "":
		hr.Route, hr.Result = best, HostRejected
	default:
		hr.Route, hr.Result = best, HostRouted
		if dev := snap.FindInterface(best.Dev); dev != nil && dev.IsDown() {
			hr.Result, hr.EgressDown = HostEgressDown, true
		}
	}
	return hr
}

// isRoutedViaNode reports whether a network's traffic passes the node's
// routing table. Only the cluster pod network is treated as routed; Multus
// networks (including a Multus default network) are typically switched at
// layer 2 by their CNI plugin.
func isRoutedViaNode(t *topology.Topology, network string) bool {
	for _, n := range t.Networks {
		if n.Name == network {
			return n.Source == topology.SourcePod
		}
	}
	return false
}
