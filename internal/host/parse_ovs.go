package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ovsTable is the output of `ovs-vsctl --format=json --data=json list <table>`.
type ovsTable struct {
	Headings []string            `json:"headings"`
	Data     [][]json.RawMessage `json:"data"`
}

func decodeTable(data []byte) ([]map[string]json.RawMessage, error) {
	var t ovsTable
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing ovs-vsctl JSON: %w", err)
	}
	rows := make([]map[string]json.RawMessage, 0, len(t.Data))
	for _, r := range t.Data {
		if len(r) != len(t.Headings) {
			return nil, fmt.Errorf("ovs-vsctl row has %d columns, expected %d", len(r), len(t.Headings))
		}
		row := map[string]json.RawMessage{}
		for i, h := range t.Headings {
			row[h] = r[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// OVSDB JSON encodes atoms as plain values, ["uuid", id], ["set", [...]] and
// ["map", [[k, v], ...]].

func ovsAtoms(raw json.RawMessage) ([]json.RawMessage, error) {
	var tagged []json.RawMessage
	if err := json.Unmarshal(raw, &tagged); err == nil && len(tagged) == 2 {
		var tag string
		if json.Unmarshal(tagged[0], &tag) == nil {
			switch tag {
			case "set":
				var items []json.RawMessage
				if err := json.Unmarshal(tagged[1], &items); err != nil {
					return nil, err
				}
				var out []json.RawMessage
				for _, it := range items {
					atoms, err := ovsAtoms(it)
					if err != nil {
						return nil, err
					}
					out = append(out, atoms...)
				}
				return out, nil
			case "uuid", "named-uuid":
				return []json.RawMessage{tagged[1]}, nil
			}
		}
	}
	return []json.RawMessage{raw}, nil
}

func ovsString(raw json.RawMessage) string {
	atoms, err := ovsAtoms(raw)
	if err != nil || len(atoms) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(atoms[0], &s) != nil {
		return ""
	}
	return s
}

func ovsStrings(raw json.RawMessage) []string {
	atoms, err := ovsAtoms(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range atoms {
		var s string
		if json.Unmarshal(a, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func ovsInts(raw json.RawMessage) []int {
	atoms, err := ovsAtoms(raw)
	if err != nil {
		return nil
	}
	var out []int
	for _, a := range atoms {
		var n int
		if json.Unmarshal(a, &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func ovsMap(raw json.RawMessage) map[string]string {
	var tagged []json.RawMessage
	if json.Unmarshal(raw, &tagged) != nil || len(tagged) != 2 {
		return nil
	}
	var tag string
	if json.Unmarshal(tagged[0], &tag) != nil || tag != "map" {
		return nil
	}
	var pairs [][2]string
	if json.Unmarshal(tagged[1], &pairs) != nil || len(pairs) == 0 {
		return nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p[0]] = p[1]
	}
	return out
}

// ParseOVSDB builds the OVS model from the Bridge, Port and Interface tables
// (each from `ovs-vsctl --format=json --data=json --columns=... list <table>`).
func ParseOVSDB(bridges, ports, interfaces []byte) (*OVS, error) {
	bRows, err := decodeTable(bridges)
	if err != nil {
		return nil, fmt.Errorf("Bridge table: %w", err)
	}
	pRows, err := decodeTable(ports)
	if err != nil {
		return nil, fmt.Errorf("Port table: %w", err)
	}
	iRows, err := decodeTable(interfaces)
	if err != nil {
		return nil, fmt.Errorf("Interface table: %w", err)
	}

	ifaceByUUID := map[string]string{}
	ovs := &OVS{}
	for _, r := range iRows {
		iface := OVSInterface{
			Name:        ovsString(r["name"]),
			Type:        ovsString(r["type"]),
			AdminState:  ovsString(r["admin_state"]),
			LinkState:   ovsString(r["link_state"]),
			MAC:         ovsString(r["mac_in_use"]),
			Error:       ovsString(r["error"]),
			ExternalIDs: ovsMap(r["external_ids"]),
		}
		if v := ovsInts(r["ofport"]); len(v) > 0 {
			iface.OFPort = v[0]
		}
		if v := ovsInts(r["mtu"]); len(v) > 0 {
			mtu := v[0]
			iface.MTU = &mtu
		}
		ifaceByUUID[ovsString(r["_uuid"])] = iface.Name
		ovs.Interfaces = append(ovs.Interfaces, iface)
	}

	portByUUID := map[string]int{}
	for _, r := range pRows {
		p := OVSPort{
			Name:        ovsString(r["name"]),
			VLANMode:    ovsString(r["vlan_mode"]),
			BondMode:    ovsString(r["bond_mode"]),
			LACP:        ovsString(r["lacp"]),
			Trunks:      ovsInts(r["trunks"]),
			ExternalIDs: ovsMap(r["external_ids"]),
		}
		if v := ovsInts(r["tag"]); len(v) > 0 {
			tag := v[0]
			p.Tag = &tag
		}
		for _, u := range ovsStrings(r["interfaces"]) {
			if name, ok := ifaceByUUID[u]; ok {
				p.Interfaces = append(p.Interfaces, name)
			}
		}
		sort.Strings(p.Interfaces)
		portByUUID[ovsString(r["_uuid"])] = len(ovs.Ports)
		ovs.Ports = append(ovs.Ports, p)
	}

	for _, r := range bRows {
		b := OVSBridge{
			Name:         ovsString(r["name"]),
			FailMode:     ovsString(r["fail_mode"]),
			DatapathType: ovsString(r["datapath_type"]),
		}
		for _, u := range ovsStrings(r["ports"]) {
			if idx, ok := portByUUID[u]; ok {
				b.Ports = append(b.Ports, ovs.Ports[idx].Name)
				ovs.Ports[idx].Bridge = b.Name
			}
		}
		sort.Strings(b.Ports)
		ovs.Bridges = append(ovs.Bridges, b)
	}
	sort.Slice(ovs.Bridges, func(i, j int) bool { return ovs.Bridges[i].Name < ovs.Bridges[j].Name })
	return ovs, nil
}

// ParseOVSBondShow parses `ovs-appctl bond/show`. Both "member" (current)
// and "slave" (older releases) wording are accepted.
func ParseOVSBondShow(data []byte) []OVSBond {
	var bonds []OVSBond
	var cur *OVSBond
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "---- ") && strings.HasSuffix(line, " ----"):
			bonds = append(bonds, OVSBond{Name: strings.TrimSpace(strings.Trim(line, "-"))})
			cur = &bonds[len(bonds)-1]
		case cur == nil:
			continue
		case strings.HasPrefix(line, "bond_mode:"):
			cur.Mode = strings.TrimSpace(strings.TrimPrefix(line, "bond_mode:"))
		case strings.HasPrefix(line, "lacp_status:"):
			cur.LACPStatus = strings.TrimSpace(strings.TrimPrefix(line, "lacp_status:"))
		case strings.HasPrefix(line, "member ") || strings.HasPrefix(line, "slave "):
			// "member eth1: enabled"
			rest := line[strings.Index(line, " ")+1:]
			name, state, ok := strings.Cut(rest, ":")
			if !ok {
				continue
			}
			cur.Members = append(cur.Members, OVSBondMember{
				Name:    strings.TrimSpace(name),
				Enabled: strings.TrimSpace(state) == "enabled",
			})
		}
	}
	return bonds
}

// MergeOVSLACPShow adds per-member LACP state and partner system IDs from
// `ovs-appctl lacp/show` to bonds parsed from bond/show.
func MergeOVSLACPShow(bonds []OVSBond, data []byte) []OVSBond {
	index := map[string]int{}
	for i, b := range bonds {
		index[b.Name] = i
	}
	var bond, member string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "---- ") && strings.HasSuffix(line, " ----"):
			bond, member = strings.TrimSpace(strings.Trim(line, "-")), ""
			if _, ok := index[bond]; !ok {
				index[bond] = len(bonds)
				bonds = append(bonds, OVSBond{Name: bond})
			}
		case bond == "":
			continue
		case strings.HasPrefix(line, "member:") || strings.HasPrefix(line, "slave:"):
			// "member: eth1: current attached"
			_, rest, _ := strings.Cut(line, ":")
			name, state, _ := strings.Cut(rest, ":")
			member = strings.TrimSpace(name)
			m := memberOf(&bonds[index[bond]], member)
			m.LACPState = strings.TrimSpace(state)
		case member != "" && strings.HasPrefix(line, "partner sys_id:"):
			m := memberOf(&bonds[index[bond]], member)
			m.PartnerSysID = strings.TrimSpace(strings.TrimPrefix(line, "partner sys_id:"))
		}
	}
	return bonds
}

func memberOf(b *OVSBond, name string) *OVSBondMember {
	for i := range b.Members {
		if b.Members[i].Name == name {
			return &b.Members[i]
		}
	}
	b.Members = append(b.Members, OVSBondMember{Name: name})
	return &b.Members[len(b.Members)-1]
}

// ParseOVSFlows summarizes `ovs-ofctl dump-flows <bridge>` output.
func ParseOVSFlows(data []byte) FlowSummary {
	var s FlowSummary
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.Contains(line, "actions=") {
			continue // header lines such as "NXST_FLOW reply" or "OFPST_FLOW reply"
		}
		s.Count++
		actions := line[strings.Index(line, "actions=")+len("actions="):]
		for _, a := range strings.Split(actions, ",") {
			if strings.EqualFold(strings.TrimSpace(a), "NORMAL") {
				s.HasNormal = true
			}
		}
	}
	return s
}
