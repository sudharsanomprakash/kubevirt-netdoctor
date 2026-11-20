package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/bundle"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/capability"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

// FullReport combines everything netdoctor knows about one VM.
type FullReport struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	ToolVersion   string                 `json:"toolVersion"`
	Diagnosis     *output.Report         `json:"diagnosis"`
	Topology      *output.TopologyReport `json:"topology"`
	Capabilities  *capability.Report     `json:"capabilities"`
}

type reportOptions struct {
	diagnoseOptions
	bundle string
	redact bool
	now    func() time.Time
}

func newReportCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &reportOptions{now: time.Now}
	o.clusterOptions = clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}
	cmd := &cobra.Command{
		Use:   "report (vm|vmi)/NAME",
		Short: "Produce a complete report for a VM, optionally as a shareable bundle",
		Long: `Report runs diagnose, builds the topology graph and checks your access, then
prints a combined report. With --bundle it also writes a .tar.gz with the
diagnosis (JSON and text), topology (JSON, text, DOT, Mermaid), capabilities,
copies of the input files and a manifest with SHA-256 checksums.

--redact replaces IP and MAC addresses with stable placeholders in every
file, including the copied inputs. Names of hosts, nodes, pods, namespaces
and other objects are not redacted; review a bundle before sharing it.`,
		Example: `  kubectl netdoctor report vm/web-01 -n demo -o json
  kubectl netdoctor report vm/web-01 -n demo --host-snapshot worker-1.json --trace trace.json --bundle web-01.tar.gz --redact`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args)
		},
	}
	o.addFlags(cmd)
	addFailOnFlag(cmd, &o.failOn)
	f := cmd.Flags()
	f.StringVar(&o.destination, "destination", "", "Destination IP address to analyze the route toward")
	f.StringVar(&o.hostSnapshot, "host-snapshot", "", "Host snapshot file from 'kubectl-netdoctor host collect'")
	f.StringVar(&o.trace, "trace", "", "Kernel trace file from 'kubectl-netdoctor host trace --plan'")
	f.StringVar(&o.expectations, "expectations", "", "YAML file with expected VLANs, MTUs and LACP partners")
	f.StringVar(&o.bundle, "bundle", "", "Also write a .tar.gz bundle to this path (mode 0600)")
	f.BoolVar(&o.redact, "redact", false, "Replace IP and MAC addresses with placeholders in the bundle and the printed report")
	return cmd
}

func (o *reportOptions) run(ctx context.Context, args []string) error {
	ctx, cancel := o.withTimeout(ctx)
	defer cancel()
	d, err := o.diagnose(ctx, args)
	if err != nil {
		return err
	}
	clients, err := o.newClients(o.configFlags)
	if err != nil {
		return err
	}
	ver := version.Get().Version
	topo := d.report.Topology
	full := &FullReport{
		SchemaVersion: output.SchemaVersion,
		Kind:          "FullReport",
		ToolVersion:   ver,
		Diagnosis:     d.report,
		Topology:      output.NewTopologyReport(ver, topo, d.snap, d.hp),
		Capabilities:  capability.Check(ctx, clients, topo.Target.Namespace),
	}

	var redactor *bundle.Redactor
	if o.redact {
		redactor = bundle.NewRedactor()
	}
	if o.bundle != "" {
		b, err := o.buildBundle(full, redactor)
		if err != nil {
			return err
		}
		if err := writeBundleFile(o.bundle, b); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(o.streams.ErrOut, "bundle written to %s (%d files)\n", o.bundle, len(b.Manifest().Files)+1)
	}

	var buf bytes.Buffer
	if o.output == output.FormatJSON {
		err = output.WriteJSON(&buf, full)
	} else {
		err = output.Write(&buf, output.FormatText, d.report)
	}
	if err != nil {
		return err
	}
	data := buf.Bytes()
	if redactor != nil {
		data = redactor.Redact(data)
	}
	if _, err := o.streams.Out.Write(data); err != nil {
		return err
	}
	return checkFailOn(o.failOn, d.report.Findings)
}

func (o *reportOptions) buildBundle(full *FullReport, redactor *bundle.Redactor) (*bundle.Builder, error) {
	t := full.Diagnosis.Topology.Target
	now := o.now()
	root := fmt.Sprintf("netdoctor-%s-%s-%s", t.Namespace, t.Name, now.UTC().Format("20060102T150405Z"))
	b := bundle.New(root, full.ToolVersion, fmt.Sprintf("%s %s/%s", t.Kind, t.Namespace, t.Name), now, redactor)

	render := func(fn func(*bytes.Buffer) error) ([]byte, error) {
		var buf bytes.Buffer
		err := fn(&buf)
		return buf.Bytes(), err
	}
	type entry struct {
		name, desc string
		fn         func(*bytes.Buffer) error
	}
	entries := []entry{
		{"diagnosis.json", "diagnose report (JSON)", func(w *bytes.Buffer) error { return output.WriteJSON(w, full.Diagnosis) }},
		{"diagnosis.txt", "diagnose report (text)", func(w *bytes.Buffer) error { return output.WriteText(w, full.Diagnosis) }},
		{"topology.json", "topology graph (JSON)", func(w *bytes.Buffer) error { return output.WriteJSON(w, full.Topology) }},
		{"topology.txt", "topology graph (text)", func(w *bytes.Buffer) error { return output.WriteTopology(w, output.FormatText, full.Topology) }},
		{"topology.dot", "topology graph (Graphviz)", func(w *bytes.Buffer) error { return output.WriteTopology(w, output.FormatDOT, full.Topology) }},
		{"topology.mmd", "topology graph (Mermaid)", func(w *bytes.Buffer) error { return output.WriteTopology(w, output.FormatMermaid, full.Topology) }},
		{"capabilities.json", "API availability and RBAC for the namespace", func(w *bytes.Buffer) error { return output.WriteJSON(w, full.Capabilities) }},
	}
	for _, e := range entries {
		data, err := render(e.fn)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", e.name, err)
		}
		if err := b.Add(e.name, e.desc, data); err != nil {
			return nil, err
		}
	}
	inputs := []struct{ path, name, desc string }{
		{o.hostSnapshot, "inputs/host-snapshot.json", "host snapshot given with --host-snapshot"},
		{o.trace, "inputs/trace.json", "kernel trace given with --trace"},
		{o.expectations, "inputs/expectations.yaml", "expectations given with --expectations"},
	}
	for _, in := range inputs {
		if in.path == "" {
			continue
		}
		data, err := os.ReadFile(in.path) // #nosec G304 -- explicit CLI argument, already parsed by diagnose
		if err != nil {
			return nil, err
		}
		if err := b.Add(in.name, in.desc, data); err != nil {
			return nil, err
		}
	}
	if o.hostSnapshot == "" {
		b.Note("no host snapshot: host bridges, OVS, uplinks and bonds are not included")
	}
	if o.trace == "" {
		b.Note("no kernel trace: no drops, retransmissions or resets were observed for this report")
	}
	b.Note("connectivity was not tested; netdoctor sends no traffic")
	if !o.redact {
		b.Note("not redacted: the bundle contains IP and MAC addresses")
	}
	return b, nil
}

func writeBundleFile(path string, b *bundle.Builder) error {
	if !strings.HasSuffix(path, ".tar.gz") && !strings.HasSuffix(path, ".tgz") {
		return fmt.Errorf("--bundle %q must end in .tar.gz or .tgz", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- explicit CLI argument
	if err != nil {
		return fmt.Errorf("creating bundle: %w", err)
	}
	if err := b.Write(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
