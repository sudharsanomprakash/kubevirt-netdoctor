package host

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
)

type ipLink struct {
	Index       int      `json:"ifindex"`
	Name        string   `json:"ifname"`
	Flags       []string `json:"flags"`
	MTU         int      `json:"mtu"`
	Master      string   `json:"master"`
	Link        string   `json:"link"`
	LinkNetnsID *int     `json:"link_netnsid"`
	OperState   string   `json:"operstate"`
	Address     string   `json:"address"`
	LinkInfo    *struct {
		Kind      string          `json:"info_kind"`
		Data      json.RawMessage `json:"info_data"`
		SlaveKind string          `json:"info_slave_kind"`
		SlaveData json.RawMessage `json:"info_slave_data"`
	} `json:"linkinfo"`
}

// ParseLinks parses `ip -j -d link show`.
func ParseLinks(data []byte) ([]Interface, error) {
	var links []ipLink
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("parsing ip link JSON: %w", err)
	}
	out := make([]Interface, 0, len(links))
	for _, l := range links {
		iface := Interface{
			Name: l.Name, Index: l.Index, MTU: l.MTU, MAC: l.Address,
			OperState: l.OperState, Master: l.Master, Parent: l.Link,
			PeerNetnsID: l.LinkNetnsID,
		}
		for _, f := range l.Flags {
			if f == "UP" {
				iface.AdminUp = true
			}
		}
		if li := l.LinkInfo; li != nil {
			iface.Kind = li.Kind
			if err := decodeLinkKind(&iface, li.Kind, li.Data); err != nil {
				return nil, fmt.Errorf("interface %s: %w", l.Name, err)
			}
			if li.SlaveKind == "bond" && len(li.SlaveData) > 0 {
				var d struct {
					State        string `json:"state"`
					MIIStatus    string `json:"mii_status"`
					AggregatorID int    `json:"ad_aggregator_id"`
				}
				if err := json.Unmarshal(li.SlaveData, &d); err != nil {
					return nil, fmt.Errorf("interface %s: bond member data: %w", l.Name, err)
				}
				iface.BondMember = &BondMemberInfo{MIIStatus: d.MIIStatus, State: d.State, AggregatorID: d.AggregatorID}
			}
		}
		out = append(out, iface)
	}
	return out, nil
}

func decodeLinkKind(iface *Interface, kind string, data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	switch kind {
	case "vlan":
		var d struct {
			ID       int    `json:"id"`
			Protocol string `json:"protocol"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("vlan data: %w", err)
		}
		iface.VLAN = &VLANInfo{ID: d.ID, Protocol: d.Protocol}
	case "bridge":
		var d struct {
			VLANFiltering int `json:"vlan_filtering"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("bridge data: %w", err)
		}
		iface.Bridge = &BridgeInfo{VLANFiltering: d.VLANFiltering == 1}
	case "bond":
		var d struct {
			Mode   string `json:"mode"`
			ADInfo *struct {
				Aggregator int    `json:"aggregator"`
				PartnerMAC string `json:"partner_mac"`
			} `json:"ad_info"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("bond data: %w", err)
		}
		iface.Bond = &BondInfo{Mode: d.Mode}
		if d.ADInfo != nil {
			iface.Bond.ActiveAggregator = d.ADInfo.Aggregator
			iface.Bond.PartnerMAC = d.ADInfo.PartnerMAC
		}
	}
	return nil
}

// ParseAddresses parses `ip -j addr show` and returns addresses (with prefix
// length) keyed by interface name.
func ParseAddresses(data []byte) (map[string][]string, error) {
	var entries []struct {
		Name     string `json:"ifname"`
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing ip addr JSON: %w", err)
	}
	out := map[string][]string{}
	for _, e := range entries {
		for _, a := range e.AddrInfo {
			if a.Local == "" {
				continue
			}
			out[e.Name] = append(out[e.Name], fmt.Sprintf("%s/%d", a.Local, a.PrefixLen))
		}
	}
	return out, nil
}

// ParseRoutes parses `ip -j route show table all` (IPv4 or IPv6). Routes
// without a "table" attribute belong to the main table.
func ParseRoutes(data []byte, ipv6 bool) ([]Route, error) {
	var entries []struct {
		Type     string `json:"type"`
		Dst      string `json:"dst"`
		Gateway  string `json:"gateway"`
		Dev      string `json:"dev"`
		Protocol string `json:"protocol"`
		Metric   int    `json:"metric"`
		Table    string `json:"table"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing ip route JSON: %w", err)
	}
	out := make([]Route, 0, len(entries))
	for _, e := range entries {
		dst, err := normalizeDst(e.Dst, ipv6)
		if err != nil {
			continue // unparseable entries (e.g. multicast types) are not needed for lookup
		}
		table := e.Table
		if table == "" {
			table = "main"
		}
		typ := e.Type
		if typ == "unicast" {
			typ = ""
		}
		out = append(out, Route{Table: table, Dst: dst, Gateway: e.Gateway, Dev: e.Dev, Protocol: e.Protocol, Metric: e.Metric, Type: typ})
	}
	return out, nil
}

func normalizeDst(dst string, ipv6 bool) (string, error) {
	if dst == "default" {
		if ipv6 {
			return "::/0", nil
		}
		return "0.0.0.0/0", nil
	}
	if p, err := netip.ParsePrefix(dst); err == nil {
		return p.Masked().String(), nil
	}
	a, err := netip.ParseAddr(dst)
	if err != nil {
		return "", err
	}
	return netip.PrefixFrom(a, a.BitLen()).String(), nil
}

// ParseBridgeVLANs parses `bridge -j vlan show`. Both the list form
// ([{"ifname":..,"vlans":[..]}]) and the older map form ({"port":[..]}) are
// accepted.
func ParseBridgeVLANs(data []byte) ([]BridgePortVLAN, error) {
	type vlanEntry struct {
		VLAN    int      `json:"vlan"`
		VLANEnd int      `json:"vlanEnd"`
		Flags   []string `json:"flags"`
	}
	convert := func(port string, entries []vlanEntry) BridgePortVLAN {
		bp := BridgePortVLAN{Port: port}
		for _, e := range entries {
			v := PortVLANs{Min: e.VLAN, Max: e.VLAN}
			if e.VLANEnd > e.VLAN {
				v.Max = e.VLANEnd
			}
			for _, f := range e.Flags {
				switch f {
				case "PVID":
					v.PVID = true
				case "Egress Untagged":
					v.Untagged = true
				}
			}
			bp.VLANs = append(bp.VLANs, v)
		}
		return bp
	}

	var list []struct {
		Name  string      `json:"ifname"`
		VLANs []vlanEntry `json:"vlans"`
	}
	if err := json.Unmarshal(data, &list); err == nil {
		out := make([]BridgePortVLAN, 0, len(list))
		for _, l := range list {
			out = append(out, convert(l.Name, l.VLANs))
		}
		return out, nil
	}
	var byPort map[string][]vlanEntry
	if err := json.Unmarshal(data, &byPort); err != nil {
		return nil, fmt.Errorf("parsing bridge vlan JSON: %w", err)
	}
	ports := make([]string, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	out := make([]BridgePortVLAN, 0, len(ports))
	for _, p := range ports {
		out = append(out, convert(p, byPort[p]))
	}
	return out, nil
}

// ParseNamespaces parses `ip -j netns list`.
func ParseNamespaces(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil // ip prints nothing when no named namespaces exist
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing ip netns JSON: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out, nil
}
