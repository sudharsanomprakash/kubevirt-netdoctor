package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

func minimalTopology() *topology.Topology {
	return &topology.Topology{
		Target:   topology.Target{Kind: topology.KindVMI, Namespace: "demo", Name: "vm"},
		Instance: &topology.VMIInfo{Name: "vm", Lookup: kube.StateFound, Phase: "Running", NetworkSpecSource: topology.SpecFromVMI},
		Launcher: &topology.LauncherInfo{Lookup: kube.StateNotFound},
	}
}

func TestNewReportCountsAndScope(t *testing.T) {
	r := NewReport("v0.0.0-test", minimalTopology(), []diagnose.Finding{
		{Severity: diagnose.SeverityError}, {Severity: diagnose.SeverityWarning},
		{Severity: diagnose.SeverityWarning}, {Severity: diagnose.SeverityInfo},
	})
	if r.Summary.Errors != 1 || r.Summary.Warnings != 2 || r.Summary.Info != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
	if r.Summary.Connectivity != ConnectivityNotTested {
		t.Errorf("connectivity must be %q, got %q", ConnectivityNotTested, r.Summary.Connectivity)
	}
}

// With no findings the report must still not claim the network is healthy.
func TestNoFindingsNeverClaimsHealthy(t *testing.T) {
	r := NewReport("v0.0.0-test", minimalTopology(), []diagnose.Finding{})
	var text, js bytes.Buffer
	if err := Write(&text, FormatText, r); err != nil {
		t.Fatal(err)
	}
	if err := Write(&js, FormatJSON, r); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": text.String(), "json": js.String()} {
		lower := strings.ToLower(out)
		for _, word := range []string{"healthy", "connectivity ok", "reachable"} {
			if strings.Contains(lower, word) {
				t.Errorf("%s output contains %q:\n%s", name, word, out)
			}
		}
	}
	if !strings.Contains(text.String(), "Connectivity: NOT TESTED") {
		t.Errorf("text output must state connectivity was not tested:\n%s", text.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if findings, ok := decoded["findings"].([]any); !ok || len(findings) != 0 {
		t.Errorf("findings must serialize as an empty array, got %#v", decoded["findings"])
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, "yaml", NewReport("v", minimalTopology(), nil)); err == nil {
		t.Error("expected an error")
	}
}
