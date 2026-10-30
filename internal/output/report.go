// Package output renders inspection reports as human-readable text or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/graph"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// SchemaVersion versions the JSON report. Breaking changes require a new
// version.
const SchemaVersion = "netdoctor/v1alpha1"

// ConnectivityNotTested is the only connectivity state netdoctor reports:
// no command sends traffic.
const ConnectivityNotTested = "NotTested"

// Report kinds.
const (
	KindInspection = "InspectionReport"
	KindDiagnosis  = "DiagnosisReport"
	KindTopology   = "TopologyReport"
)

// Report is the result of inspect or diagnose. The host and route sections
// are present only for diagnose when the corresponding inputs were given.
type Report struct {
	SchemaVersion string             `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	ToolVersion   string             `json:"toolVersion"`
	Topology      *topology.Topology `json:"topology"`
	HostSnapshot  *SnapshotInfo      `json:"hostSnapshot,omitempty"`
	Host          *hostpath.Result   `json:"host,omitempty"`
	Route         *route.Analysis    `json:"route,omitempty"`
	Findings      []diagnose.Finding `json:"findings"`
	Summary       Summary            `json:"summary"`
}

// SnapshotInfo identifies the host snapshot used and what it contains.
type SnapshotInfo struct {
	NodeName    string              `json:"nodeName"`
	CollectedAt string              `json:"collectedAt,omitempty"`
	Sources     []host.SourceStatus `json:"sources"`
}

// NewSnapshotInfo summarizes a snapshot; nil in, nil out.
func NewSnapshotInfo(s *host.Snapshot) *SnapshotInfo {
	if s == nil {
		return nil
	}
	return &SnapshotInfo{NodeName: s.NodeName, CollectedAt: s.CollectedAt, Sources: s.Sources}
}

// TopologyReport is the result of the topology command.
type TopologyReport struct {
	SchemaVersion string             `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	ToolVersion   string             `json:"toolVersion"`
	Topology      *topology.Topology `json:"topology"`
	HostSnapshot  *SnapshotInfo      `json:"hostSnapshot,omitempty"`
	Host          *hostpath.Result   `json:"host,omitempty"`
	Graph         *graph.Graph       `json:"graph"`
	Scope         string             `json:"scope"`
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

// hostScope describes a run that included a host snapshot.
func hostScope(info *SnapshotInfo, matched bool) string {
	if info == nil {
		return scope
	}
	if !matched {
		return fmt.Sprintf("Read-only Kubernetes API inspection. The host snapshot (node %s) is not from the VM's node and was not used. Guest and switch state were not observed.", info.NodeName)
	}
	return fmt.Sprintf("Read-only Kubernetes API inspection plus a host snapshot of node %s collected at %s. Host state may have changed since; configuration does not prove forwarding. Guest and switch state were not observed.",
		info.NodeName, info.CollectedAt)
}

// NewReport assembles a report.
func NewReport(toolVersion string, topo *topology.Topology, findings []diagnose.Finding) *Report {
	r := &Report{
		SchemaVersion: SchemaVersion,
		Kind:          KindInspection,
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

// NewDiagnosisReport assembles a diagnose report.
func NewDiagnosisReport(toolVersion string, topo *topology.Topology, snap *host.Snapshot, hp *hostpath.Result,
	ra *route.Analysis, findings []diagnose.Finding) *Report {
	r := NewReport(toolVersion, topo, findings)
	r.Kind = KindDiagnosis
	r.HostSnapshot, r.Host, r.Route = NewSnapshotInfo(snap), hp, ra
	r.Summary.Scope = hostScope(r.HostSnapshot, hp != nil && hp.NodeMatch)
	return r
}

// NewTopologyReport assembles a topology report.
func NewTopologyReport(toolVersion string, topo *topology.Topology, snap *host.Snapshot, hp *hostpath.Result) *TopologyReport {
	info := NewSnapshotInfo(snap)
	return &TopologyReport{
		SchemaVersion: SchemaVersion,
		Kind:          KindTopology,
		ToolVersion:   toolVersion,
		Topology:      topo,
		HostSnapshot:  info,
		Host:          hp,
		Graph:         graph.Build(topo, hp),
		Scope:         hostScope(info, hp != nil && hp.NodeMatch),
	}
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
