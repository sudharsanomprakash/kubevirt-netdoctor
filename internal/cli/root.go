// Package cli implements the kubectl-netdoctor command line.
package cli

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// ClientFactory builds API clients from kubectl configuration flags. Tests
// substitute a factory that returns fake clients.
type ClientFactory func(genericclioptions.RESTClientGetter) (*kube.Clients, error)

// NewRootCommand builds the kubectl-netdoctor command tree.
func NewRootCommand(streams genericiooptions.IOStreams, newClients ClientFactory) *cobra.Command {
	configFlags := genericclioptions.NewConfigFlags(true)

	root := &cobra.Command{
		Use:   "kubectl-netdoctor",
		Short: "Diagnose KubeVirt virtual machine networking",
		Long: `kubectl netdoctor inspects how a KubeVirt virtual machine is wired into the
cluster network: its VMI interfaces, virt-launcher pod, Multus attachments and
NetworkAttachmentDefinitions. It reads Kubernetes API state only and never
modifies the cluster. Connectivity is not tested.`,
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: "kubectl netdoctor",
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)
	configFlags.AddFlags(root.PersistentFlags())

	root.AddCommand(
		newInspectCommand(streams, configFlags, newClients),
		newVersionCommand(streams),
	)
	return root
}
