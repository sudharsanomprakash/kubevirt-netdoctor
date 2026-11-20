package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/capability"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
)

func newCapabilitiesCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Show which APIs and permissions netdoctor can use in a namespace",
		Long: `Capabilities checks API discovery and runs SelfSubjectAccessReviews for every
read netdoctor performs, then states which features work with your
credentials. It reads nothing else and changes nothing.`,
		Example: `  kubectl netdoctor capabilities -n demo`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			ctx, cancel := o.withTimeout(cmd.Context())
			defer cancel()
			namespace, _, err := configFlags.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return fmt.Errorf("resolving namespace: %w", err)
			}
			clients, err := newClients(configFlags)
			if err != nil {
				return err
			}
			r := capability.Check(ctx, clients, namespace)
			if o.output == output.FormatJSON {
				return output.WriteJSON(streams.Out, r)
			}
			return writeClusterCaps(streams.Out, r)
		},
	}
	o.addFlags(cmd)
	return cmd
}

func writeClusterCaps(w io.Writer, r *capability.Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n\nAPIS\n", r.Namespace)
	for _, a := range r.APIs {
		req := "optional"
		if a.Required {
			req = "required"
		}
		fmt.Fprintf(&b, "  %-12s %-50s %-8s %s\n", a.Served, a.Resource, req, a.Purpose)
	}
	b.WriteString("\nACCESS\n")
	for _, a := range r.Access {
		line := fmt.Sprintf("  %-12s %-5s %s", a.Allowed, a.Verb, a.Resource)
		if a.Reason != "" {
			line += "  (" + a.Reason + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nFEATURES\n")
	for _, f := range r.Features {
		line := fmt.Sprintf("  %-12s %s", f.Status, f.Name)
		if f.Detail != "" {
			line += ": " + f.Detail
		}
		b.WriteString(line + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func newHostCapabilitiesCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	var (
		out  string
		load bool
	)
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Check whether this node supports eBPF tracing and host collection",
		Long: `Host capabilities checks the kernel (BTF, tracepoints, drop reasons), the
process's privileges and kernel symbol visibility. By default it also loads
netdoctor's BPF programs through the kernel verifier to prove they are
accepted; nothing is attached and they are unloaded immediately.`,
		Example: `  kubectl-netdoctor host capabilities
  kubectl-netdoctor host capabilities --load=false -o json`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			nc := env.Probe(load)
			switch out {
			case output.FormatJSON:
				return output.WriteJSON(streams.Out, nc)
			case output.FormatText:
				return writeNodeCaps(streams.Out, nc)
			default:
				return fmt.Errorf("unsupported output format %q", out)
			}
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", output.FormatText, "Output format: text or json")
	cmd.Flags().BoolVar(&load, "load", true, "Load the BPF programs through the verifier (needs root); nothing is attached")
	return cmd
}

func writeNodeCaps(w io.Writer, nc ebpf.NodeCapabilities) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Kernel: %s (%s)\neBPF tracing: %s\n\n", dashIfEmpty(nc.Kernel), dashIfEmpty(nc.Arch), nc.Tracing)
	for _, c := range nc.Checks {
		line := fmt.Sprintf("  %-12s %s", c.Status, c.Name)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		if c.Impact != "" {
			line += " (" + c.Impact + ")"
		}
		b.WriteString(line + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
