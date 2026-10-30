package multus

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// IPAM is the addressing configuration declared in a plugin's "ipam" block.
// Only the subset needed for route analysis is modeled. Fields are empty when
// the IPAM type assigns addresses at runtime (for example "dhcp") or is not
// recognized.
type IPAM struct {
	Type string `json:"type"`
	// Subnets are the prefixes addresses are allocated from (host-local
	// ranges, whereabouts ranges, or the networks of static addresses).
	Subnets []string `json:"subnets,omitempty"`
	// Addresses are statically assigned addresses with prefix length.
	Addresses []string `json:"addresses,omitempty"`
	Gateways  []string `json:"gateways,omitempty"`
	Routes    []Route  `json:"routes,omitempty"`
	// Dynamic is set for IPAM types whose addressing is only known at runtime.
	Dynamic bool `json:"dynamic,omitempty"`
}

// Route is a route declared in IPAM configuration. An empty GW means the
// route uses the attachment's default gateway, if any.
type Route struct {
	Dst string `json:"dst"`
	GW  string `json:"gw,omitempty"`
}

// VLANRange is an inclusive VLAN ID range from a trunk definition.
type VLANRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Contains reports whether the VLAN ID is within the range.
func (r VLANRange) Contains(vlan int) bool { return vlan >= r.Min && vlan <= r.Max }

// ParsedSubnets returns the IPAM subnets as prefixes, skipping invalid ones
// (parsing already rejected invalid prefixes for strict IPAM types).
func (i *IPAM) ParsedSubnets() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range i.Subnets {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// parseIPAM interprets the "ipam" object. Invalid addressing for IPAM types
// whose format is well defined (host-local, static) is an error, because the
// plugin would reject it too; for other types problems become warnings.
func parseIPAM(fields map[string]json.RawMessage) (*IPAM, []string, error) {
	ipam := &IPAM{}
	if err := optionalString(fields, "type", &ipam.Type); err != nil {
		return nil, nil, fmt.Errorf("ipam: %w", err)
	}
	var warnings []string
	switch ipam.Type {
	case "host-local":
		if err := parseHostLocal(fields, ipam); err != nil {
			return nil, nil, fmt.Errorf("ipam (host-local): %w", err)
		}
	case "static":
		if err := parseStatic(fields, ipam); err != nil {
			return nil, nil, fmt.Errorf("ipam (static): %w", err)
		}
	case "whereabouts":
		warnings = parseWhereabouts(fields, ipam)
	case "dhcp":
		ipam.Dynamic = true
	}
	routes, err := parseRoutes(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("ipam: %w", err)
	}
	ipam.Routes = routes
	return ipam, warnings, nil
}

type rangeEntry struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}

func parseHostLocal(fields map[string]json.RawMessage, ipam *IPAM) error {
	var entries []rangeEntry
	if raw, ok := fields["ranges"]; ok && !isJSONNull(raw) {
		var sets [][]rangeEntry
		if err := json.Unmarshal(raw, &sets); err != nil {
			return fmt.Errorf(`"ranges" must be a list of range sets: %w`, err)
		}
		for _, set := range sets {
			entries = append(entries, set...)
		}
	}
	// Legacy single-range form.
	var legacy rangeEntry
	if err := optionalString(fields, "subnet", &legacy.Subnet); err != nil {
		return err
	}
	if err := optionalString(fields, "gateway", &legacy.Gateway); err != nil {
		return err
	}
	if legacy.Subnet != "" {
		entries = append(entries, legacy)
	}
	for _, e := range entries {
		p, err := netip.ParsePrefix(e.Subnet)
		if err != nil {
			return fmt.Errorf("invalid subnet %q", e.Subnet)
		}
		ipam.Subnets = appendUnique(ipam.Subnets, p.Masked().String())
		if e.Gateway != "" {
			gw, err := netip.ParseAddr(e.Gateway)
			if err != nil || !p.Contains(gw) {
				return fmt.Errorf("gateway %q is not an address in subnet %s", e.Gateway, p)
			}
			ipam.Gateways = appendUnique(ipam.Gateways, gw.String())
		}
	}
	return nil
}

func parseStatic(fields map[string]json.RawMessage, ipam *IPAM) error {
	raw, ok := fields["addresses"]
	if !ok || isJSONNull(raw) {
		return nil
	}
	var addrs []struct {
		Address string `json:"address"`
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(raw, &addrs); err != nil {
		return fmt.Errorf(`"addresses" must be a list of objects: %w`, err)
	}
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.Address)
		if err != nil {
			return fmt.Errorf("invalid address %q (expected address/prefix)", a.Address)
		}
		ipam.Addresses = append(ipam.Addresses, p.String())
		ipam.Subnets = appendUnique(ipam.Subnets, p.Masked().String())
		if a.Gateway != "" {
			gw, err := netip.ParseAddr(a.Gateway)
			if err != nil {
				return fmt.Errorf("invalid gateway %q", a.Gateway)
			}
			ipam.Gateways = appendUnique(ipam.Gateways, gw.String())
		}
	}
	return nil
}

// parseWhereabouts reads "range" (CIDR, optionally "start-end/len") and
// "ipRanges". Unparseable values become warnings because the format has
// varied across whereabouts releases.
func parseWhereabouts(fields map[string]json.RawMessage, ipam *IPAM) []string {
	var warnings []string
	var ranges []string
	var single string
	if err := optionalString(fields, "range", &single); err == nil && single != "" {
		ranges = append(ranges, single)
	}
	if raw, ok := fields["ipRanges"]; ok && !isJSONNull(raw) {
		var list []struct {
			Range string `json:"range"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			warnings = append(warnings, `whereabouts "ipRanges" could not be parsed`)
		}
		for _, r := range list {
			ranges = append(ranges, r.Range)
		}
	}
	for _, r := range ranges {
		cidr := r
		if dash := strings.Index(r, "-"); dash >= 0 {
			// "192.0.2.10-192.0.2.20/24": keep the start address and prefix length.
			if slash := strings.LastIndex(r, "/"); slash > dash {
				cidr = r[:dash] + r[slash:]
			}
		}
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("whereabouts range %q could not be parsed", r))
			continue
		}
		ipam.Subnets = appendUnique(ipam.Subnets, p.Masked().String())
	}
	var gw string
	if err := optionalString(fields, "gateway", &gw); err == nil && gw != "" {
		if a, err := netip.ParseAddr(gw); err == nil {
			ipam.Gateways = appendUnique(ipam.Gateways, a.String())
		} else {
			warnings = append(warnings, fmt.Sprintf("whereabouts gateway %q is not an IP address", gw))
		}
	}
	return warnings
}

func parseRoutes(fields map[string]json.RawMessage) ([]Route, error) {
	raw, ok := fields["routes"]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	var routes []Route
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, fmt.Errorf(`"routes" must be a list of {"dst","gw"} objects: %w`, err)
	}
	for i, r := range routes {
		p, err := netip.ParsePrefix(r.Dst)
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: invalid dst %q", i, r.Dst)
		}
		routes[i].Dst = p.Masked().String()
		if r.GW != "" {
			gw, err := netip.ParseAddr(r.GW)
			if err != nil {
				return nil, fmt.Errorf("routes[%d]: invalid gw %q", i, r.GW)
			}
			routes[i].GW = gw.String()
		}
	}
	return routes, nil
}

// parseTrunk reads a trunk definition shared by Linux bridge CNI
// ("vlanTrunk") and OVS CNI ("trunk"): a list of {"id": N} or
// {"minID": A, "maxID": B} objects.
func parseTrunk(raw json.RawMessage, key string) ([]VLANRange, error) {
	var items []struct {
		ID    *int `json:"id"`
		MinID *int `json:"minID"`
		MaxID *int `json:"maxID"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%q must be a list of {id} or {minID,maxID} objects", key)
	}
	var out []VLANRange
	for i, it := range items {
		var r VLANRange
		switch {
		case it.ID != nil:
			r = VLANRange{Min: *it.ID, Max: *it.ID}
		case it.MinID != nil && it.MaxID != nil:
			r = VLANRange{Min: *it.MinID, Max: *it.MaxID}
		default:
			return nil, fmt.Errorf("%s[%d]: needs \"id\" or both \"minID\" and \"maxID\"", key, i)
		}
		if r.Min < 0 || r.Max > 4094 || r.Min > r.Max {
			return nil, fmt.Errorf("%s[%d]: VLAN range %d-%d is invalid (0-4094)", key, i, r.Min, r.Max)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Min < out[j].Min })
	return out, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
