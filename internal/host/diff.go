package host

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Change is one field that differs between two snapshots. An empty Before or
// After means the object was absent on that side.
type Change struct {
	Object string `json:"object"`
	Name   string `json:"name"`
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// SnapshotDiff is the result of comparing two host snapshots. Volatile fields
// (timestamps, counters) are not compared.
type SnapshotDiff struct {
	NodeBefore string   `json:"nodeBefore"`
	NodeAfter  string   `json:"nodeAfter"`
	Changes    []Change `json:"changes"`
}

// DiffSnapshots compares two snapshots by object name and field. It is pure
// and does not read anything from the host.
func DiffSnapshots(before, after *Snapshot) SnapshotDiff {
	d := SnapshotDiff{NodeBefore: before.NodeName, NodeAfter: after.NodeName}

	diffInterfaces(&d, before.Interfaces, after.Interfaces)
	diffBridgeVLANs(&d, before.BridgeVLANs, after.BridgeVLANs)
	diffRoutes(&d, before.Routes, after.Routes)
	diffOVS(&d, before.OVS, after.OVS)

	sort.SliceStable(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Field < b.Field
	})
	return d
}

func (d *SnapshotDiff) add(object, name, field, before, after string) {
	if before == after {
		return
	}
	d.Changes = append(d.Changes, Change{Object: object, Name: name, Field: field, Before: before, After: after})
}

func diffInterfaces(d *SnapshotDiff, before, after []Interface) {
	old := map[string]Interface{}
	for _, i := range before {
		old[i.Name] = i
	}
	seen := map[string]bool{}
	for _, a := range after {
		seen[a.Name] = true
		b, ok := old[a.Name]
		if !ok {
			d.add("interface", a.Name, "present", "no", "yes")
			continue
		}
		d.add("interface", a.Name, "mtu", itoa(b.MTU), itoa(a.MTU))
		d.add("interface", a.Name, "kind", b.Kind, a.Kind)
		d.add("interface", a.Name, "mac", b.MAC, a.MAC)
		d.add("interface", a.Name, "operState", b.OperState, a.OperState)
		d.add("interface", a.Name, "adminUp", strconv.FormatBool(b.AdminUp), strconv.FormatBool(a.AdminUp))
		d.add("interface", a.Name, "master", b.Master, a.Master)
		d.add("interface", a.Name, "parent", b.Parent, a.Parent)
		d.add("interface", a.Name, "addresses", strings.Join(b.Addresses, ","), strings.Join(a.Addresses, ","))
		d.add("interface", a.Name, "vlan", vlanString(b.VLAN), vlanString(a.VLAN))
		d.add("interface", a.Name, "bridge.vlanFiltering", bridgeString(b.Bridge), bridgeString(a.Bridge))
		d.add("interface", a.Name, "bond", bondString(b.Bond), bondString(a.Bond))
		d.add("interface", a.Name, "bondMember", bondMemberString(b.BondMember), bondMemberString(a.BondMember))
	}
	for _, b := range before {
		if !seen[b.Name] {
			d.add("interface", b.Name, "present", "yes", "no")
		}
	}
}

func vlanString(v *VLANInfo) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d/%s", v.ID, v.Protocol)
}

func bridgeString(b *BridgeInfo) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(b.VLANFiltering)
}

func bondString(b *BondInfo) string {
	if b == nil {
		return ""
	}
	return fmt.Sprintf("%s/%d/%s", b.Mode, b.ActiveAggregator, b.PartnerMAC)
}

func bondMemberString(m *BondMemberInfo) string {
	if m == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%d/%s", m.MIIStatus, m.State, m.AggregatorID, m.PartnerMAC)
}

func diffBridgeVLANs(d *SnapshotDiff, before, after []BridgePortVLAN) {
	old := map[string]string{}
	for _, p := range before {
		old[p.Port] = vlanSetString(p.VLANs)
	}
	seen := map[string]bool{}
	for _, p := range after {
		seen[p.Port] = true
		d.add("bridge-vlan", p.Port, "vlans", old[p.Port], vlanSetString(p.VLANs))
	}
	for _, p := range before {
		if !seen[p.Port] {
			d.add("bridge-vlan", p.Port, "vlans", vlanSetString(p.VLANs), "")
		}
	}
}

func vlanSetString(vlans []PortVLANs) string {
	parts := make([]string, 0, len(vlans))
	for _, v := range vlans {
		s := fmt.Sprintf("%d-%d", v.Min, v.Max)
		if v.PVID {
			s += "/pvid"
		}
		if v.Untagged {
			s += "/untagged"
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func diffRoutes(d *SnapshotDiff, before, after []Route) {
	key := func(r Route) string { return r.Table + " " + r.Dst + " " + r.Type }
	old := map[string]Route{}
	for _, r := range before {
		old[key(r)] = r
	}
	seen := map[string]bool{}
	for _, a := range after {
		k := key(a)
		seen[k] = true
		b, ok := old[k]
		name := strings.TrimSpace(a.Table + " " + a.Dst)
		if !ok {
			d.add("route", name, "present", "no", "yes")
			continue
		}
		d.add("route", name, "gateway", b.Gateway, a.Gateway)
		d.add("route", name, "dev", b.Dev, a.Dev)
		d.add("route", name, "protocol", b.Protocol, a.Protocol)
		d.add("route", name, "metric", itoa(b.Metric), itoa(a.Metric))
	}
	for _, b := range before {
		if !seen[key(b)] {
			d.add("route", strings.TrimSpace(b.Table+" "+b.Dst), "present", "yes", "no")
		}
	}
}

func diffOVS(d *SnapshotDiff, before, after *OVS) {
	if before == nil && after == nil {
		return
	}
	if before == nil {
		before = &OVS{}
	}
	if after == nil {
		after = &OVS{}
	}

	oldBridges := map[string]OVSBridge{}
	for _, b := range before.Bridges {
		oldBridges[b.Name] = b
	}
	seenBridges := map[string]bool{}
	for _, a := range after.Bridges {
		seenBridges[a.Name] = true
		b, ok := oldBridges[a.Name]
		if !ok {
			d.add("ovs-bridge", a.Name, "present", "no", "yes")
			continue
		}
		d.add("ovs-bridge", a.Name, "failMode", b.FailMode, a.FailMode)
		d.add("ovs-bridge", a.Name, "datapathType", b.DatapathType, a.DatapathType)
	}
	for _, b := range before.Bridges {
		if !seenBridges[b.Name] {
			d.add("ovs-bridge", b.Name, "present", "yes", "no")
		}
	}

	portKey := func(p OVSPort) string { return p.Bridge + "/" + p.Name }
	oldPorts := map[string]OVSPort{}
	for _, p := range before.Ports {
		oldPorts[portKey(p)] = p
	}
	seenPorts := map[string]bool{}
	for _, a := range after.Ports {
		k := portKey(a)
		seenPorts[k] = true
		b, ok := oldPorts[k]
		if !ok {
			d.add("ovs-port", k, "present", "no", "yes")
			continue
		}
		d.add("ovs-port", k, "tag", intPtrString(b.Tag), intPtrString(a.Tag))
		d.add("ovs-port", k, "trunks", intsString(b.Trunks), intsString(a.Trunks))
		d.add("ovs-port", k, "vlanMode", b.VLANMode, a.VLANMode)
		d.add("ovs-port", k, "bondMode", b.BondMode, a.BondMode)
		d.add("ovs-port", k, "lacp", b.LACP, a.LACP)
	}
	for _, b := range before.Ports {
		if !seenPorts[portKey(b)] {
			d.add("ovs-port", portKey(b), "present", "yes", "no")
		}
	}

	bondKey := func(b OVSBond) string { return b.Name }
	oldBonds := map[string]OVSBond{}
	for _, b := range before.Bonds {
		oldBonds[bondKey(b)] = b
	}
	seenBonds := map[string]bool{}
	for _, a := range after.Bonds {
		k := bondKey(a)
		seenBonds[k] = true
		b, ok := oldBonds[k]
		if !ok {
			d.add("ovs-bond", k, "present", "no", "yes")
			continue
		}
		d.add("ovs-bond", k, "mode", b.Mode, a.Mode)
		d.add("ovs-bond", k, "lacpStatus", b.LACPStatus, a.LACPStatus)
	}
	for _, b := range before.Bonds {
		if !seenBonds[bondKey(b)] {
			d.add("ovs-bond", bondKey(b), "present", "yes", "no")
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func intPtrString(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func intsString(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
