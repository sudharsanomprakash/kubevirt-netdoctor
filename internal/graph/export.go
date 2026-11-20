package graph

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Edge styles by provenance, shared by both exports: observed solid,
// declared dashed, inferred dotted.
func dotStyle(p string) string {
	switch p {
	case Declared:
		return "dashed"
	case Inferred:
		return "dotted"
	default:
		return "solid"
	}
}

func dotShape(kind string) string {
	switch kind {
	case KindVM, KindVMI:
		return "box3d"
	case KindPod, KindNode:
		return "box"
	case KindNAD:
		return "note"
	case KindHostBridge, KindOVSBridge:
		return "hexagon"
	case KindBond:
		return "doubleoctagon"
	case KindClusterNetwork:
		return "cylinder"
	default:
		return "ellipse"
	}
}

func label(n Node) string {
	return n.Kind + "\n" + n.Name
}

// WriteDOT renders the graph in Graphviz DOT.
func WriteDOT(w io.Writer, g *Graph, title string) error {
	var b strings.Builder
	b.WriteString("digraph netdoctor {\n")
	fmt.Fprintf(&b, "  label=%s;\n  labelloc=t;\n  rankdir=LR;\n  node [fontname=\"Helvetica\", fontsize=10];\n  edge [fontname=\"Helvetica\", fontsize=9];\n", dotQuote(title))
	for _, n := range sortedNodes(g) {
		fmt.Fprintf(&b, "  %s [label=%s, shape=%s];\n", dotQuote(n.ID), dotQuote(label(n)), dotShape(n.Kind))
	}
	for _, e := range g.Edges {
		fmt.Fprintf(&b, "  %s -> %s [label=%s, style=%s, tooltip=%s];\n",
			dotQuote(e.From), dotQuote(e.To), dotQuote(e.Relation+" ("+e.Provenance+")"), dotStyle(e.Provenance), dotQuote(e.Evidence))
	}
	writeNotes(&b, g, "  // ")
	b.WriteString("}\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func dotQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// WriteMermaid renders the graph as a Mermaid flowchart.
func WriteMermaid(w io.Writer, g *Graph, title string) error {
	var b strings.Builder
	// The title is YAML front matter: quote it so colons are literal.
	fmt.Fprintf(&b, "---\ntitle: \"%s\"\n---\nflowchart LR\n", strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(title))
	ids := map[string]string{}
	for i, n := range sortedNodes(g) {
		id := fmt.Sprintf("n%d", i)
		ids[n.ID] = id
		open, closing := mermaidShape(n.Kind)
		fmt.Fprintf(&b, "  %s%s\"%s<br/>%s\"%s\n", id, open, mermaidText(n.Kind), mermaidText(n.Name), closing)
	}
	for _, e := range g.Edges {
		from, to := ids[e.From], ids[e.To]
		if from == "" || to == "" {
			continue
		}
		arrow, lbl := "-->", e.Relation
		switch e.Provenance {
		case Declared:
			arrow, lbl = "-.->", lbl+" (declared)"
		case Inferred:
			arrow, lbl = "-.->", lbl+" (inferred)"
		}
		fmt.Fprintf(&b, "  %s %s|\"%s\"| %s\n", from, arrow, mermaidText(lbl), to)
	}
	writeNotes(&b, g, "  %% ")
	_, err := io.WriteString(w, b.String())
	return err
}

func mermaidShape(kind string) (string, string) {
	switch kind {
	case KindVM, KindVMI, KindPod, KindNode:
		return "[", "]"
	case KindNAD:
		return "[/", "/]"
	case KindHostBridge, KindOVSBridge:
		return "{{", "}}"
	case KindClusterNetwork:
		return "[(", ")]"
	default:
		return "(", ")"
	}
}

// mermaidText escapes characters Mermaid treats as syntax inside quotes.
func mermaidText(s string) string {
	r := strings.NewReplacer(`"`, "#quot;", "<", "#lt;", ">", "#gt;", "\n", " ")
	return r.Replace(s)
}

func writeNotes(b *strings.Builder, g *Graph, prefix string) {
	for _, gap := range g.Gaps {
		fmt.Fprintf(b, "%sgap: %s\n", prefix, strings.ReplaceAll(gap, "\n", " "))
	}
	for _, a := range g.Absent {
		fmt.Fprintf(b, "%sabsent: %s\n", prefix, strings.ReplaceAll(a, "\n", " "))
	}
}

func sortedNodes(g *Graph) []Node {
	nodes := append([]Node(nil), g.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes
}
