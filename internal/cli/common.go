package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/inspect"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

const defaultTimeout = 60 * time.Second

// clusterOptions are shared by commands that inspect a VM through the API.
type clusterOptions struct {
	streams     genericiooptions.IOStreams
	configFlags *genericclioptions.ConfigFlags
	newClients  ClientFactory
	output      string
	timeout     time.Duration
}

func (o *clusterOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.output, "output", "o", output.FormatText, "Output format: text or json")
	cmd.Flags().DurationVar(&o.timeout, "timeout", defaultTimeout, "Maximum time for the whole command")
}

func (o *clusterOptions) validate() error {
	if o.output != output.FormatText && o.output != output.FormatJSON {
		return fmt.Errorf("unsupported output format %q (use %q or %q)", o.output, output.FormatText, output.FormatJSON)
	}
	return nil
}

// discover resolves the target and namespace and runs the read-only API
// discovery. Callers bound ctx with withTimeout.
func (o *clusterOptions) discover(ctx context.Context, args []string) (*topology.Topology, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	kind, name, err := parseTarget(args)
	if err != nil {
		return nil, err
	}
	namespace, _, err := o.configFlags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, fmt.Errorf("resolving namespace: %w", err)
	}
	clients, err := o.newClients(o.configFlags)
	if err != nil {
		return nil, err
	}
	return inspect.Inspect(ctx, clients, topology.Target{Kind: kind, Namespace: namespace, Name: name})
}

// withTimeout derives the command context.
func (o *clusterOptions) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.timeout > 0 {
		return context.WithTimeout(ctx, o.timeout)
	}
	return context.WithCancel(ctx)
}

// loadHost loads an optional host snapshot and correlates it.
func loadHost(path string, topo *topology.Topology) (*host.Snapshot, *hostpath.Result, error) {
	if path == "" {
		return nil, nil, nil
	}
	snap, err := host.LoadSnapshot(path)
	if err != nil {
		return nil, nil, err
	}
	return snap, hostpath.Correlate(topo, snap), nil
}

// matchingSnapshot returns the snapshot only when it describes the VM's node.
func matchingSnapshot(snap *host.Snapshot, hp *hostpath.Result) *host.Snapshot {
	if hp == nil || !hp.NodeMatch {
		return nil
	}
	return snap
}
