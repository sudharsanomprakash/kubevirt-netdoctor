// Package output renders inspection reports as human-readable text or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// SchemaVersion versions the JSON report. Breaking changes require a new
// version.
const SchemaVersion = "netdoctor/v1alpha1"

// ConnectivityNotTested is the only connectivity state v0.1 can report.
const ConnectivityNotTested = "NotTested"

// Report is the complete result of an inspection.
type Report struct {
	SchemaVersion string             `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	ToolVersion   string             `json:"toolVersion"`
	Topology      *topology.Topology `json:"topology"`
	Findings      []diagnose.Finding `json:"findings"`
	Summary       Summary            `json:"summary"`
}

// Summary aggregates findings and states the scope of the inspection.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Info     int `json:"info"`
	// Connectivity is always NotTested in v0.1: the inspection only reads
	// API state and never sends traffic.
	Connectivity string `json:"connectivity"`
	Scope        string `json:"scope"`
}

const scope = "Read-only Kubernetes API inspection. Guest, node, OVS/bridge, host NIC and switch state were not observed."

// NewReport assembles a report.
func NewReport(toolVersion string, topo *topology.Topology, findings []diagnose.Finding) *Report {
	r := &Report{
		SchemaVersion: SchemaVersion,
		Kind:          "InspectionReport",
		ToolVersion:   toolVersion,
		Topology:      topo,
		Findings:      findings,
		Summary:       Summary{Connectivity: ConnectivityNotTested, Scope: scope},
	}
	for _, f := range findings {
		switch f.Severity {
		case diagnose.SeverityError:
			r.Summary.Errors++
		case diagnose.SeverityWarning:
			r.Summary.Warnings++
		default:
			r.Summary.Info++
		}
	}
	return r
}

// Formats.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Write renders the report in the given format.
func Write(w io.Writer, format string, r *Report) error {
	switch format {
	case FormatJSON:
		return WriteJSON(w, r)
	case FormatText, "":
		return WriteText(w, r)
	default:
		return fmt.Errorf("unsupported output format %q (use %q or %q)", format, FormatText, FormatJSON)
	}
}

// WriteJSON renders any value as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
