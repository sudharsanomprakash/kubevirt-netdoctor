package cli

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/expect"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

type diagnoseOptions struct {
	clusterOptions
	destination  string
	hostSnapshot string
	expectations string
}

func newDiagnoseCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &diagnoseOptions{clusterOptions: clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}}
	cmd := &cobra.Command{
		Use:   "diagnose (vm|vmi)/NAME [--destination IP]",
		Short: "Run all diagnostics for a VM, optionally analyzing the route to a destination",
		Long: `Diagnose runs every rule: the Kubernetes API checks of inspect, host and OVS
checks when --host-snapshot is given, expectation checks when --expectations
is given, and configuration and route analysis toward --destination.

Diagnose sends no traffic. Route analysis uses declared NAD IPAM subnets and
routes, KubeVirt binding semantics and, with a host snapshot, the node's main
routing table. The guest's own routing table is not observable.`,
		Example: `  kubectl netdoctor diagnose vm/web-01 -n demo --destination 198.51.100.20
  kubectl netdoctor diagnose vm/web-01 -n demo --host-snapshot worker-1.json --expectations expect.yaml -o json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args)
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&o.destination, "destination", "", "Destination IP address to analyze the route toward")
	cmd.Flags().StringVar(&o.hostSnapshot, "host-snapshot", "", "Host snapshot file from 'kubectl netdoctor host collect' (experimental)")
	cmd.Flags().StringVar(&o.expectations, "expectations", "", "YAML file with expected VLANs, MTUs and LACP partners (see docs/findings.md)")
	return cmd
}

func (o *diagnoseOptions) run(ctx context.Context, args []string) error {
	var dest netip.Addr
	if o.destination != "" {
		var err error
		if dest, err = netip.ParseAddr(o.destination); err != nil {
			return fmt.Errorf("invalid --destination %q: expected an IP address", o.destination)
		}
	}
	var exp *expect.Spec
	if o.expectations != "" {
		var err error
		if exp, err = expect.Load(o.expectations); err != nil {
			return err
		}
	}

	ctx, cancel := o.withTimeout(ctx)
	defer cancel()
	topo, err := o.discover(ctx, args)
	if err != nil {
		return err
	}
	snap, hp, err := loadHost(o.hostSnapshot, topo)
	if err != nil {
		return err
	}
	var ra *route.Analysis
	if dest.IsValid() {
		ra = route.Analyze(topo, dest, matchingSnapshot(snap, hp))
	}
	in := &diagnose.Input{Topology: topo, Snapshot: snap, Host: hp, Expect: exp, Route: ra}
	findings := diagnose.Evaluate(in, diagnose.AllRules())
	return output.Write(o.streams.Out, o.output, output.NewDiagnosisReport(version.Get().Version, topo, snap, hp, ra, findings))
}
