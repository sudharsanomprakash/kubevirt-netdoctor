package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
)

// HostEnv is the environment the host collector runs in. Tests replace it.
type HostEnv struct {
	Runner   host.Runner
	ProcRoot string
	Hostname func() (string, error)
}

// DefaultHostEnv runs real commands against the local host.
func DefaultHostEnv() HostEnv {
	return HostEnv{Runner: host.ExecRunner{}, ProcRoot: "/", Hostname: os.Hostname}
}

func newHostCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Host network inspection (experimental)",
	}
	cmd.AddCommand(newHostCollectCommand(streams, env))
	return cmd
}

func newHostCollectCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	var (
		nodeName, file   string
		skipOVS, noFlows bool
		cmdTimeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "collect",
		Short: "Collect a read-only snapshot of this node's network state (experimental)",
		Long: `Collect runs on a Kubernetes node and records its network state for
'diagnose --host-snapshot' and 'topology --host-snapshot'. It does not
contact the Kubernetes API and must be started explicitly by an operator on
the node; netdoctor never deploys it.

It runs only this fixed set of read-only commands, without a shell:
  ip -j -d link show | ip -j addr show | ip -j -4/-6 route show table all
  bridge -j vlan show | ip -j netns list
  ovs-vsctl --format=json list Bridge|Port|Interface (selected columns)
  ovs-appctl bond/show | ovs-appctl lacp/show | ovs-ofctl dump-flows <bridge>
and reads /proc/net/bonding/<bond>.

Linux link, address and route state is readable without privileges. OVS
and /proc/net/bonding usually require root. Sources that cannot be read are
recorded as Failed in the snapshot instead of aborting.`,
		Example: `  # On the node (for example in a debug session), as root:
  kubectl-netdoctor host collect --node-name worker-1 --file /tmp/worker-1.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hostname, _ := env.Hostname()
			if nodeName == "" {
				nodeName = hostname
			}
			if nodeName == "" {
				return fmt.Errorf("could not determine the node name; pass --node-name")
			}
			snap, err := host.Collect(cmd.Context(), env.Runner, os.DirFS(env.ProcRoot), host.CollectOptions{
				NodeName: nodeName, Hostname: hostname, SkipOVS: skipOVS, SkipFlows: noFlows, CommandTimeout: cmdTimeout,
			})
			if err != nil {
				return err
			}
			w := streams.Out
			if file != "" {
				f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- explicit CLI argument
				if err != nil {
					return fmt.Errorf("creating snapshot file: %w", err)
				}
				defer func() { _ = f.Close() }()
				w = f
			}
			if err := output.WriteJSON(w, snap); err != nil {
				return err
			}
			for _, src := range snap.Sources {
				if src.Status == host.StatusFailed {
					_, _ = fmt.Fprintf(streams.ErrOut, "warning: source %s failed: %s\n", src.Name, src.Message)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&nodeName, "node-name", "", "Kubernetes node name to record (defaults to the hostname)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "Write the snapshot to this file (mode 0600) instead of stdout")
	cmd.Flags().BoolVar(&skipOVS, "skip-ovs", false, "Do not collect Open vSwitch state")
	cmd.Flags().BoolVar(&noFlows, "skip-flows", false, "Do not summarize OpenFlow tables")
	cmd.Flags().DurationVar(&cmdTimeout, "command-timeout", 10*time.Second, "Timeout for each command")
	return cmd
}
