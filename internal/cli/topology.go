package cli

import (
	"context"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

type topologyOptions struct {
	clusterOptions
	hostSnapshot string
}

func newTopologyCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &topologyOptions{clusterOptions: clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}}
	cmd := &cobra.Command{
		Use:   "topology (vm|vmi)/NAME",
		Short: "Show the network topology of a VM as a graph of observed, declared and inferred relationships",
		Long: `Topology builds a graph of the VM's network path: VMI networks, guest and pod
interfaces, NetworkAttachmentDefinitions and, when a host snapshot of the VM's
node is supplied, host bridges, OVS ports, uplinks and bonds.

Every relationship is labeled Observed (reported by the API or the host),
Declared (stated by configuration) or Inferred (derived), and layers that
could not be observed are listed as gaps.`,
		Example: `  kubectl netdoctor topology vm/web-01 -n demo
  kubectl netdoctor topology vm/web-01 -n demo --host-snapshot worker-1.json -o json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args)
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&o.hostSnapshot, "host-snapshot", "", "Host snapshot file from 'kubectl netdoctor host collect' (experimental)")
	return cmd
}

func (o *topologyOptions) run(ctx context.Context, args []string) error {
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
	return output.WriteTopology(o.streams.Out, o.output, output.NewTopologyReport(version.Get().Version, topo, snap, hp))
}
