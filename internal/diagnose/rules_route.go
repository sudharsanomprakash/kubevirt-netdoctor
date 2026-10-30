package diagnose

import (
	"fmt"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
)

// Finding IDs for destination analysis.
const (
	IDRouteMissing        = "ROUTE_MISSING"
	IDRouteUndetermined   = "ROUTE_UNDETERMINED"
	IDRouteAmbiguous      = "ROUTE_AMBIGUOUS"
	IDRouteGatewayOffLink = "ROUTE_GATEWAY_OFFLINK"
	IDHostRouteMissing    = "HOST_ROUTE_MISSING"
	IDHostRouteRejected   = "HOST_ROUTE_REJECTED"
	IDHostRouteEgressDown = "HOST_ROUTE_EGRESS_DOWN"
)

// RouteRules evaluate a destination analysis.
func RouteRules() []Rule {
	return []Rule{{ID: "route", Description: "A route to the destination is configured", Check: checkRoute}}
}

func checkRoute(in *Input) []Finding {
	a := in.Route
	if a == nil {
		return nil
	}
	res := vmiResource(in.Topology)
	var out []Finding
	switch {
	case len(a.Candidates) == 0 && len(a.Undeclared) > 0:
		var ev []string
		for _, u := range a.Undeclared {
			ev = append(ev, fmt.Sprintf("network %q: guest reports %s; %s", u.Network, strings.Join(u.GuestIPs, ", "), u.Reason))
		}
		ev = append(ev, "KubeVirt reports guest addresses without prefix lengths, so on-link reachability cannot be evaluated")
		out = append(out, Finding{
			ID: IDRouteUndetermined, Severity: SeverityInfo, Category: Unavailable, Resource: res,
			Summary:  fmt.Sprintf("The path to %s cannot be determined from configuration; addressing is configured inside the guest", a.Destination),
			Evidence: ev,
			NextAction: "Check inside the guest: ip route get " + a.Destination +
				" (or use the guest agent), and the switch/VLAN the destination is on.",
		})
	case len(a.Candidates) == 0:
		out = append(out, Finding{
			ID: IDRouteMissing, Severity: SeverityWarning, Category: Suspected, Resource: res,
			Summary: fmt.Sprintf("No declared subnet or route of any VM network covers %s", a.Destination),
			Evidence: []string{
				"no NAD IPAM subnet or route contains the destination",
				"no pod network binding that provides a default route was found",
			},
			Expected:   "a network whose declared addressing or routes reach the destination",
			NextAction: "Check the guest's own routing table (it is not observable here) and the NAD IPAM routes.",
		})
	case a.Ambiguous:
		var ev []string
		for _, c := range a.Candidates {
			ev = append(ev, fmt.Sprintf("%s via network %q (%s, %s)", c.Prefix, c.Network, c.Kind, c.Provenance))
		}
		out = append(out, Finding{
			ID: IDRouteAmbiguous, Severity: SeverityWarning, Category: Suspected, Resource: res,
			Summary:    fmt.Sprintf("Several VM networks offer equally specific routes to %s", a.Destination),
			Evidence:   ev,
			NextAction: "The guest's route metrics decide; check them inside the guest.",
		})
	}
	if s := a.Selected; s != nil && s.GatewayOffLink {
		out = append(out, Finding{
			ID: IDRouteGatewayOffLink, Severity: SeverityError, Category: Confirmed, Resource: res,
			Summary:    fmt.Sprintf("The gateway %s for %s on network %q is outside the network's declared subnets", s.Gateway, s.Prefix, s.Network),
			Evidence:   []string{s.Reason},
			Expected:   "gateway inside a declared IPAM subnet (NAD configuration)",
			NextAction: "Correct the IPAM gateway or route in the NAD.",
		})
	}

	h := a.Host
	if h == nil || !h.Applicable {
		return out
	}
	node := "Node"
	if in.Snapshot != nil {
		node = snapshotResource(in)
	}
	switch h.Result {
	case route.HostNoRoute:
		out = append(out, Finding{
			ID: IDHostRouteMissing, Severity: SeverityError, Category: Confirmed, Resource: node,
			Summary:    fmt.Sprintf("The node's main routing table has no route to %s", a.Destination),
			Evidence:   []string{h.Reason},
			Expected:   "a route (or default route) covering the destination",
			NextAction: "Check the node's default route and the CNI's egress path (policy routing tables are not evaluated).",
		})
	case route.HostRejected:
		out = append(out, Finding{
			ID: IDHostRouteRejected, Severity: SeverityError, Category: Confirmed, Resource: node,
			Summary:    fmt.Sprintf("The node routes %s to a %s route", a.Destination, h.Route.Type),
			Evidence:   []string{routeString(h)},
			Expected:   "a unicast route",
			NextAction: "Find who installed the " + h.Route.Type + " route (ip route show " + h.Route.Dst + ").",
		})
	case route.HostEgressDown:
		out = append(out, Finding{
			ID: IDHostRouteEgressDown, Severity: SeverityError, Category: Confirmed, Resource: node,
			Summary:    fmt.Sprintf("The node's route to %s uses interface %s, which is down", a.Destination, h.Route.Dev),
			Evidence:   []string{routeString(h)},
			Expected:   "egress interface up",
			NextAction: "Check the state of " + h.Route.Dev + " on the node.",
		})
	}
	return out
}

func routeString(h *route.HostRoute) string {
	r := h.Route
	parts := []string{"route " + r.Dst}
	if r.Gateway != "" {
		parts = append(parts, "via "+r.Gateway)
	}
	if r.Dev != "" {
		parts = append(parts, "dev "+r.Dev)
	}
	if r.Type != "" {
		parts = append(parts, "type "+r.Type)
	}
	return strings.Join(parts, " ")
}
