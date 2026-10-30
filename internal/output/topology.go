package output

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/graph"
)

// WriteTopology renders a topology report in the given format.
func WriteTopology(w io.Writer, format string, r *TopologyReport) error {
	switch format {
	case FormatJSON:
		return WriteJSON(w, r)
	case FormatText, "":
		return writeTopologyText(w, r)
	default:
		return fmt.Errorf("unsupported output format %q (use %q or %q)", format, FormatText, FormatJSON)
	}
}

// writeTopologyText walks the graph depth-first from each VMI network, so
// the text shows exactly the relationships present in the JSON graph.
func writeTopologyText(w io.Writer, r *TopologyReport) error {
	p := &printer{w: w}
	g := r.Graph
	t := r.Topology

	p.line("Topology:  %s %s/%s", t.Target.Kind, t.Target.Namespace, t.Target.Name)
	if t.Instance != nil {
		p.line("VMI:       phase=%s  node=%s", dash(t.Instance.Phase), dash(t.Instance.NodeName))
	}
	if t.Launcher != nil && t.Launcher.Selected != nil {
		p.line("Launcher:  %s", t.Launcher.Selected.Name)
	}
	if r.HostSnapshot != nil {
		p.line("Host:      snapshot of node %s collected %s", r.HostSnapshot.NodeName, dash(r.HostSnapshot.CollectedAt))
	}

	nodes := map[string]graph.Node{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	out := map[string][]graph.Edge{}
	for _, e := range g.Edges {
		out[e.From] = append(out[e.From], e)
	}
	visited := map[string]bool{}
	var walk func(id, indent string)
	walk = func(id, indent string) {
		if visited[id] {
			return
		}
		visited[id] = true
		for _, e := range out[id] {
			to := nodes[e.To]
			mark := ""
			if visited[e.To] {
				mark = " (shown above)"
			}
			p.line("%s+- %s -> %s %s%s  [%s] %s", indent, e.Relation, to.Kind, to.Name, attrString(to.Attributes), e.Provenance, e.Evidence+mark)
			walk(e.To, indent+"|  ")
		}
	}

	for _, n := range g.Nodes {
		if n.Kind != graph.KindNetwork {
			continue
		}
		p.blank()
		name := n.Name[strings.LastIndex(n.Name, "/")+1:]
		p.line("network %s%s", name, attrString(n.Attributes))
		walk(n.ID, "  ")
	}

	var stray []graph.Node
	for _, n := range g.Nodes {
		if n.Kind == graph.KindPodIface && !visited[n.ID] {
			stray = append(stray, n)
		}
	}
	if len(stray) > 0 {
		p.blank()
		p.line("POD INTERFACES NOT ATTRIBUTED TO A VMI NETWORK")
		for _, n := range stray {
			p.line("  %s%s", n.Name, attrString(n.Attributes))
		}
	}

	p.blank()
	p.line("ABSENT (%d): sources were readable but did not contain these", len(g.Absent))
	for _, a := range g.Absent {
		p.line("  - %s", a)
	}
	p.blank()
	p.line("GAPS (%d): not observable", len(g.Gaps))
	for _, gap := range g.Gaps {
		p.line("  - %s", gap)
	}
	p.blank()
	p.line("Provenance: Observed = reported by the Kubernetes API or host snapshot; Declared = stated by configuration; Inferred = derived from documented behavior or matching heuristics.")
	p.line("Connectivity: NOT TESTED. %s", r.Scope)
	return p.err
}

func attrString(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+attrs[k])
	}
	return " {" + strings.Join(parts, ", ") + "}"
}
