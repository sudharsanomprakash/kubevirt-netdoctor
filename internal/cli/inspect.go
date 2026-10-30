package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

func newInspectCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}
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
			return runInspect(cmd.Context(), o, args)
		},
	}
	o.addFlags(cmd)
	return cmd
}

func runInspect(ctx context.Context, o *clusterOptions, args []string) error {
	ctx, cancel := o.withTimeout(ctx)
	defer cancel()
	topo, err := o.discover(ctx, args)
	if err != nil {
		return err
	}
	findings := diagnose.Evaluate(&diagnose.Input{Topology: topo}, diagnose.DefaultRules())
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
