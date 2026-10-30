package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// WriteText renders the report for humans. Layers of each network are listed
// in packet-path order: guest, pod attachment, NetworkAttachmentDefinition.
func WriteText(w io.Writer, r *Report) error {
	p := &printer{w: w}
	t := r.Topology

	p.line("Target:    %s %s/%s", t.Target.Kind, t.Target.Namespace, t.Target.Name)
	if vm := t.VirtualMachine; vm != nil {
		if vm.Lookup == kube.StateFound {
			p.line("VM:        %s  runStrategy=%s  status=%s", vm.Name, dash(vm.RunStrategy), dash(vm.PrintableStatus))
		} else {
			p.line("VM:        %s  (%s)", vm.Name, vm.Lookup)
		}
	}
	if vmi := t.Instance; vmi != nil {
		if vmi.Lookup == kube.StateFound {
			p.line("VMI:       %s  phase=%s  node=%s  guestAgent=%s", vmi.Name, dash(vmi.Phase), dash(vmi.NodeName), dash(vmi.GuestAgent))
		} else {
			p.line("VMI:       %s  (%s)", vmi.Name, vmi.Lookup)
		}
	}
	if l := t.Launcher; l != nil {
		switch {
		case l.Selected != nil:
			p.line("Launcher:  %s  phase=%s  node=%s", l.Selected.Name, l.Selected.Phase, dash(l.Selected.NodeName))
		case l.Lookup == kube.StateNotChecked:
			p.line("Launcher:  not checked (%s)", l.SelectionReason)
		default:
			p.line("Launcher:  none selected (%s)", dash(l.SelectionReason))
		}
	}

	p.blank()
	p.line("NETWORKS (%d)", len(t.Networks))
	if t.Instance != nil && t.Instance.NetworkSpecSource == topology.SpecFromVMTemplate {
		p.line("  (from the VM template; no runtime state is available)")
	}
	for _, n := range t.Networks {
		writeNetwork(p, n, t.Launcher)
	}
	for _, g := range t.UnmatchedGuestInterfaces {
		p.line("  %s  [status only, not in spec]", g.Name)
		p.line("    guest:  %s", guestLine(g))
	}

	if r.HostSnapshot != nil {
		writeHost(p, r)
	}
	if r.Route != nil {
		writeRoute(p, r.Route)
	}

	p.blank()
	p.line("FINDINGS (%d): %d error(s), %d warning(s), %d info", len(r.Findings), r.Summary.Errors, r.Summary.Warnings, r.Summary.Info)
	if len(r.Findings) == 0 {
		p.line("  No issues were found in the inspected state.")
	}
	for _, f := range r.Findings {
		writeFinding(p, f)
	}

	p.blank()
	p.line("Connectivity: NOT TESTED. %s", r.Summary.Scope)
	return p.err
}

func writeNetwork(p *printer, n topology.NetworkPath, l *topology.LauncherInfo) {
	header := n.Source
	switch {
	case n.Source == topology.SourceMultus && n.NAD != nil && n.NAD.RefError == "":
		header = "multus " + n.NAD.Ref.String()
		if n.MultusDefault {
			header += " (default network)"
		}
	case n.Source == topology.SourcePod:
		header = "pod network"
	}
	binding := "-"
	if n.Interface != nil {
		binding = n.Interface.Binding
	}
	p.line("  %s  [%s, binding: %s]", n.Name, header, binding)

	if n.Guest != nil {
		p.line("    guest:  %s", guestLine(*n.Guest))
	} else {
		p.line("    guest:  not reported")
	}

	switch {
	case n.PodAttachment != nil:
		a := n.PodAttachment
		parts := []string{"iface " + dash(a.Interface), "network " + a.NetworkName}
		if a.MAC != "" {
			parts = append(parts, "mac "+a.MAC)
		}
		if len(a.IPs) > 0 {
			parts = append(parts, "ips "+strings.Join(a.IPs, ","))
		}
		p.line("    pod:    %s (Multus network-status)", strings.Join(parts, "  "))
	case n.PodRequest != nil:
		p.line("    pod:    requested iface %s; not in Multus network-status", dash(n.PodRequest.Interface))
	case l == nil || l.Selected == nil:
		p.line("    pod:    no launcher pod selected")
	case !l.NetworkStatusPresent:
		p.line("    pod:    unknown (no Multus network-status annotation)")
	default:
		p.line("    pod:    not reported")
	}

	if nad := n.NAD; nad != nil {
		switch {
		case nad.RefError != "":
			p.line("    nad:    invalid reference: %s", nad.RefError)
		case nad.Lookup != kube.StateFound:
			p.line("    nad:    %s", nad.Lookup)
		case nad.ConfigError != "":
			p.line("    nad:    found; invalid CNI config: %s", nad.ConfigError)
		case nad.ConfigNotInline:
			p.line("    nad:    found; CNI config not inline (node-local file)")
		case nad.Config != nil:
			line := "found; cni " + nad.Config.Summary()
			if prim := nad.Config.Primary(); prim != nil && prim.IPAM != "" {
				line += "; ipam " + prim.IPAM
			}
			if nad.ResourceName != "" {
				line += "; resource " + nad.ResourceName
			}
			p.line("    nad:    %s", line)
		}
	}
}

func guestLine(g topology.GuestInterface) string {
	parts := []string{"iface " + dash(g.InterfaceName)}
	if g.MAC != "" {
		parts = append(parts, "mac "+g.MAC)
	}
	if len(g.IPs) > 0 {
		parts = append(parts, "ips "+strings.Join(g.IPs, ","))
	} else {
		parts = append(parts, "ips none reported")
	}
	if g.InfoSource != "" {
		parts = append(parts, "source "+g.InfoSource)
	}
	return strings.Join(parts, "  ")
}

func writeFinding(p *printer, f diagnose.Finding) {
	p.line("  %-7s %s [%s] %s", strings.ToUpper(string(f.Severity)), f.ID, f.Category, f.Resource)
	p.line("          %s", f.Summary)
	for _, e := range f.Evidence {
		p.line("          - %s", e)
	}
	if f.Expected != "" {
		p.line("          expected: %s", f.Expected)
	}
	if f.NextAction != "" {
		p.line("          next: %s", f.NextAction)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printer writes lines and remembers the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) line(format string, args ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format+"\n", args...)
}

func (p *printer) blank() { p.line("") }

func writeHost(p *printer, r *Report) {
	p.blank()
	info := r.HostSnapshot
	p.line("HOST PATH (snapshot of node %s, collected %s)", info.NodeName, dash(info.CollectedAt))
	for _, src := range info.Sources {
		if src.Status != "OK" {
			p.line("  source %s: %s %s", src.Name, src.Status, src.Message)
		}
	}
	if r.Host == nil || !r.Host.NodeMatch {
		p.line("  not correlated: the snapshot is not from the VM's node")
		return
	}
	for _, hp := range r.Host.Paths {
		header := hp.Kind
		if hp.DeclaredDevice != "" {
			header += " " + hp.DeclaredDevice + ": " + hp.DeviceState
		}
		p.line("  %s  [%s]", hp.Network, header)
		if hp.DeviceReason != "" {
			p.line("    %s", hp.DeviceReason)
		}
		if d := hp.Device; d != nil {
			parts := []string{"type " + d.Type}
			if d.OperState != "" {
				parts = append(parts, "state "+d.OperState)
			}
			if d.MTU != nil {
				parts = append(parts, fmt.Sprintf("mtu %d", *d.MTU))
			}
			if d.VLANFiltering != nil {
				parts = append(parts, fmt.Sprintf("vlan_filtering %v", *d.VLANFiltering))
			}
			if d.FailMode != "" {
				parts = append(parts, "fail_mode "+d.FailMode)
			}
			if d.Flows != nil {
				parts = append(parts, fmt.Sprintf("%d flows", d.Flows.Count))
			}
			p.line("    device: %s", strings.Join(parts, "  "))
		}
		if pp := hp.PodPort; pp != nil {
			tag := "none"
			if pp.Tag != nil {
				tag = fmt.Sprint(*pp.Tag)
			}
			trunks := ""
			if len(pp.Trunks) > 0 {
				trunks = fmt.Sprintf("  trunks %v", pp.Trunks)
			}
			p.line("    pod port: %s on %s  tag %s%s  (%s)", pp.Name, pp.Bridge, tag, trunks, pp.MatchedBy)
		} else if hp.PodPortState != hostpath.NotApplicable {
			p.line("    pod port: %s", hp.PodPortState)
		}
		for _, u := range hp.Uplinks {
			writeUplink(p, u, "    ")
		}
		for _, n := range hp.Notes {
			p.line("    note: %s", n)
		}
	}
}

func writeUplink(p *printer, u hostpath.Uplink, indent string) {
	parts := []string{u.Type}
	if u.OperState != "" {
		parts = append(parts, u.OperState)
	}
	if u.MTU != nil {
		parts = append(parts, fmt.Sprintf("mtu %d", *u.MTU))
	}
	if u.VLANInfo != "" {
		parts = append(parts, u.VLANInfo)
	} else if len(u.VLANs) > 0 {
		var vs []string
		for _, v := range u.VLANs {
			if v.Min == v.Max {
				vs = append(vs, fmt.Sprint(v.Min))
			} else {
				vs = append(vs, fmt.Sprintf("%d-%d", v.Min, v.Max))
			}
		}
		parts = append(parts, "vlans "+strings.Join(vs, ","))
	}
	p.line("%suplink: %s (%s)", indent, u.Name, strings.Join(parts, ", "))
	if b := u.Bond; b != nil {
		var ms []string
		for _, m := range b.Members {
			s := m.Name
			if m.Detached {
				s += " DETACHED (" + m.Reason + ")"
			}
			if m.PartnerID != "" {
				s += " partner " + m.PartnerID
			}
			ms = append(ms, s)
		}
		p.line("%s  bond %s %s: %s", indent, b.Type, dash(b.Mode), strings.Join(ms, "; "))
	}
	for _, l := range u.Lower {
		writeUplink(p, l, indent+"  ")
	}
}

func writeRoute(p *printer, a *route.Analysis) {
	p.blank()
	p.line("ROUTE TO %s (configuration analysis)", a.Destination)
	switch {
	case a.Selected != nil:
		s := a.Selected
		gw := ""
		if s.Gateway != "" {
			gw = " via " + s.Gateway
		}
		p.line("  selected:  network %s, %s %s%s [%s] %s", s.Network, s.Kind, s.Prefix, gw, s.Provenance, s.Reason)
		if s.GuestEvidence != "" {
			p.line("             %s", s.GuestEvidence)
		}
	case a.Ambiguous:
		p.line("  selected:  none (equally specific candidates on several networks)")
	default:
		p.line("  selected:  none (no candidate)")
	}
	for _, u := range a.Undeclared {
		p.line("  undeclared: network %s, guest %s (%s)", u.Network, strings.Join(u.GuestIPs, ", "), u.Reason)
	}
	for _, c := range a.Candidates {
		gw := ""
		if c.Gateway != "" {
			gw = " via " + c.Gateway
		}
		p.line("  candidate: network %s, %s %s%s [%s] %s", c.Network, c.Kind, c.Prefix, gw, c.Provenance, c.Reason)
	}
	if h := a.Host; h != nil {
		line := h.Result
		if h.Route != nil {
			line += fmt.Sprintf(" (%s via %s dev %s)", h.Route.Dst, dash(h.Route.Gateway), dash(h.Route.Dev))
		}
		if !h.Applicable {
			line = "not applicable"
		}
		p.line("  node:      %s; %s", line, h.Reason)
	}
	for _, n := range a.Notes {
		p.line("  note:      %s", n)
	}
}
