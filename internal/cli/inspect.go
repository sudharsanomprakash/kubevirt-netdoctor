package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/inspect"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

const defaultTimeout = 60 * time.Second

type inspectOptions struct {
	streams     genericiooptions.IOStreams
	configFlags *genericclioptions.ConfigFlags
	newClients  ClientFactory
	output      string
	timeout     time.Duration
}

func newInspectCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &inspectOptions{streams: streams, configFlags: configFlags, newClients: newClients}
	cmd := &cobra.Command{
		Use:   "inspect (vm|vmi)/NAME",
		Short: "Inspect the network topology of a VirtualMachine or VirtualMachineInstance",
		Long: `Inspect discovers the VM/VMI, its virt-launcher pod, Multus attachments and
NetworkAttachmentDefinitions, correlates them per network, and reports
diagnostic findings. All access is read-only.`,
		Example: `  # Inspect a VirtualMachine
  kubectl netdoctor inspect vm/fedora-web -n demo

  # Inspect a VirtualMachineInstance and emit JSON
  kubectl netdoctor inspect vmi/fedora-web -n demo -o json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args)
		},
	}
	cmd.Flags().StringVarP(&o.output, "output", "o", output.FormatText, "Output format: text or json")
	cmd.Flags().DurationVar(&o.timeout, "timeout", defaultTimeout, "Maximum time for the whole inspection")
	return cmd
}

func (o *inspectOptions) run(ctx context.Context, args []string) error {
	if o.output != output.FormatText && o.output != output.FormatJSON {
		return fmt.Errorf("unsupported output format %q (use %q or %q)", o.output, output.FormatText, output.FormatJSON)
	}
	kind, name, err := parseTarget(args)
	if err != nil {
		return err
	}
	namespace, _, err := o.configFlags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return fmt.Errorf("resolving namespace: %w", err)
	}
	clients, err := o.newClients(o.configFlags)
	if err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}

	target := topology.Target{Kind: kind, Namespace: namespace, Name: name}
	topo, err := inspect.Inspect(ctx, clients, target)
	if err != nil {
		return err
	}
	findings := diagnose.Evaluate(topo, diagnose.DefaultRules())
	return output.Write(o.streams.Out, o.output, output.NewReport(version.Get().Version, topo, findings))
}

// parseTarget accepts "vm/NAME", "vmi/NAME" or "vm NAME" (and the long
// resource names), mirroring kubectl conventions.
func parseTarget(args []string) (kind, name string, err error) {
	var kindArg string
	switch len(args) {
	case 1:
		k, n, ok := strings.Cut(args[0], "/")
		if !ok {
			return "", "", fmt.Errorf("expected TYPE/NAME (for example vm/my-vm), got %q", args[0])
		}
		kindArg, name = k, n
	case 2:
		kindArg, name = args[0], args[1]
	default:
		return "", "", fmt.Errorf("expected TYPE/NAME or TYPE NAME")
	}
	if name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("invalid name %q", name)
	}
	switch strings.ToLower(kindArg) {
	case "vm", "vms", "virtualmachine", "virtualmachines", "virtualmachine.kubevirt.io", "virtualmachines.kubevirt.io":
		return topology.KindVM, name, nil
	case "vmi", "vmis", "virtualmachineinstance", "virtualmachineinstances",
		"virtualmachineinstance.kubevirt.io", "virtualmachineinstances.kubevirt.io":
		return topology.KindVMI, name, nil
	default:
		return "", "", fmt.Errorf("unsupported resource type %q: use vm or vmi", kindArg)
	}
}
