package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/version"
)

func newVersionCommand(streams genericiooptions.IOStreams) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the netdoctor version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			info := version.Get()
			switch format {
			case output.FormatJSON:
				return output.WriteJSON(streams.Out, info)
			case output.FormatText:
				_, err := fmt.Fprintf(streams.Out, "kubectl-netdoctor %s (commit %s, built %s, %s, %s)\n",
					info.Version, info.Commit, info.Date, info.GoVersion, info.Platform)
				return err
			default:
				return fmt.Errorf("unsupported output format %q (use %q or %q)", format, output.FormatText, output.FormatJSON)
			}
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", output.FormatText, "Output format: text or json")
	return cmd
}
