package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
)

// HostEnv is the environment the host collector runs in. Tests replace it.
type HostEnv struct {
	Runner   host.Runner
	ProcRoot string
	Hostname func() (string, error)
	Tracer   TracerEnv
	// Probe checks node capabilities; load runs the verifier.
	Probe func(load bool) ebpf.NodeCapabilities
}

// TracerEnv is the kernel tracer and the node lookups it needs.
type TracerEnv struct {
	Run              func(context.Context, ebpf.Options, ebpf.Meta) (*ebpf.Trace, error)
	FindPodNetns     func(podUID string) (*ebpf.PodNetns, error)
	ListInterfaces   func(nsPath string) ([]ebpf.InterfaceFilter, error)
	ResolveInterface func(nsPath, name string) (ebpf.InterfaceFilter, error)
}

// DefaultHostEnv runs real commands against the local host.
func DefaultHostEnv() HostEnv {
	return HostEnv{
		Runner: host.ExecRunner{}, ProcRoot: "/", Hostname: os.Hostname, Probe: ebpf.ProbeCapabilities,
		Tracer: TracerEnv{
			Run:              ebpf.Run,
			FindPodNetns:     func(uid string) (*ebpf.PodNetns, error) { return ebpf.FindPodNetns("/proc", uid) },
			ListInterfaces:   ebpf.ListInterfaces,
			ResolveInterface: ebpf.ResolveInterface,
		},
	}
}

func newHostCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Host network inspection (experimental)",
	}
	cmd.AddCommand(
		newHostCollectCommand(streams, env),
		newHostDiffCommand(streams),
		newHostTraceCommand(streams, env),
		newHostCapabilitiesCommand(streams, env),
	)
	return cmd
}

func newHostCollectCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	var (
		nodeName, file   string
		skipOVS, noFlows bool
		allowMismatch    bool
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
			if !host.NamesAgree(nodeName, hostname) && !allowMismatch {
				return fmt.Errorf("--node-name %q does not match this host (%q): the snapshot must be collected on the node it describes. "+
					"Run the collector on that node, or pass --allow-node-name-mismatch if the kubelet uses a different node name", nodeName, hostname)
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
	cmd.Flags().BoolVar(&allowMismatch, "allow-node-name-mismatch", false, "Allow a --node-name that differs from this host's name (kubelet --hostname-override)")
	cmd.Flags().BoolVar(&skipOVS, "skip-ovs", false, "Do not collect Open vSwitch state")
	cmd.Flags().BoolVar(&noFlows, "skip-flows", false, "Do not summarize OpenFlow tables")
	cmd.Flags().DurationVar(&cmdTimeout, "command-timeout", 10*time.Second, "Timeout for each command")
	return cmd
}

func newHostDiffCommand(streams genericiooptions.IOStreams) *cobra.Command {
	var (
		format        string
		allowMismatch bool
	)
	cmd := &cobra.Command{
		Use:   "diff before.json after.json",
		Short: "Compare two host snapshots offline (experimental)",
		Long: `Compare two snapshots written by 'host collect'. It reads only the two
files: no node and no Kubernetes API are contacted.

Changes are grouped by object (interface, bridge VLAN, route, OVS bridge,
port and bond). Volatile fields such as timestamps and counters are ignored.`,
		Example: `  kubectl-netdoctor host diff before.json after.json
  kubectl-netdoctor host diff before.json after.json -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			before, err := host.LoadSnapshot(args[0])
			if err != nil {
				return err
			}
			after, err := host.LoadSnapshot(args[1])
			if err != nil {
				return err
			}
			if before.NodeName != after.NodeName && !allowMismatch {
				return fmt.Errorf("snapshots are from different nodes (%q and %q); pass --allow-node-name-mismatch to compare them anyway",
					before.NodeName, after.NodeName)
			}
			diff := host.DiffSnapshots(before, after)
			switch format {
			case "json":
				return output.WriteJSON(streams.Out, diff)
			case "text":
				writeDiffText(streams.Out, diff)
				return nil
			default:
				return fmt.Errorf("unknown output format %q; use text or json", format)
			}
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", "text", "Output format: text or json")
	cmd.Flags().BoolVar(&allowMismatch, "allow-node-name-mismatch", false, "Compare snapshots from different nodes")
	return cmd
}

func writeDiffText(w io.Writer, diff host.SnapshotDiff) {
	if len(diff.Changes) == 0 {
		_, _ = fmt.Fprintln(w, "no differences")
		return
	}
	group := ""
	for _, c := range diff.Changes {
		next := c.Object + " " + c.Name
		if next != group {
			_, _ = fmt.Fprintln(w, next)
			group = next
		}
		before, after := c.Before, c.After
		if before == "" {
			before = "-"
		}
		if after == "" {
			after = "-"
		}
		_, _ = fmt.Fprintf(w, "  %s: %s -> %s\n", c.Field, before, after)
	}
}
