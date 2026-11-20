package graph

import (
	"bytes"
	"strings"
	"testing"
)

func TestExportEscaping(t *testing.T) {
	g := &Graph{
		Nodes: []Node{{ID: `a"1`, Kind: KindVM, Name: `we"ird<name>`}, {ID: "b", Kind: KindNAD, Name: "ns/nad"}},
		Edges: []Edge{
			{From: `a"1`, To: "b", Relation: "uses", Provenance: Declared, Evidence: `spec "x"`},
			{From: "b", To: "missing", Relation: "dangling", Provenance: Observed},
		},
		Gaps: []string{"no host\nsnapshot"},
	}
	var dot bytes.Buffer
	if err := WriteDOT(&dot, g, `t "1"`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"a\"1" [label="VirtualMachine\nwe\"ird<name>"`, `style=dashed`, `label="t \"1\""`, "// gap: no host snapshot"} {
		if !strings.Contains(dot.String(), want) {
			t.Errorf("DOT lacks %q:\n%s", want, dot.String())
		}
	}
	var mm bytes.Buffer
	if err := WriteMermaid(&mm, g, `t: "1"`); err != nil {
		t.Fatal(err)
	}
	s := mm.String()
	for _, want := range []string{`title: "t: \"1\""`, `we#quot;ird#lt;name#gt;`, `-.->|"uses (declared)"|`, "%% gap: no host snapshot"} {
		if !strings.Contains(s, want) {
			t.Errorf("Mermaid lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "dangling") {
		t.Error("edges to unknown nodes must be skipped in Mermaid")
	}
}
