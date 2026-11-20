package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/output"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/traceplan"
)

// ---- kubectl netdoctor trace (cluster side, unprivileged) ----

type traceOptions struct {
	clusterOptions
	duration time.Duration
	events   []string
	file     string
	now      func() time.Time
}

func newTraceCommand(streams genericiooptions.IOStreams, configFlags *genericclioptions.ConfigFlags, newClients ClientFactory) *cobra.Command {
	o := &traceOptions{clusterOptions: clusterOptions{streams: streams, configFlags: configFlags, newClients: newClients}, now: time.Now}
	cmd := &cobra.Command{
		Use:   "trace (vm|vmi)/NAME",
		Short: "Plan a kernel trace of a VM's traffic on its node (experimental)",
		Long: `Trace builds a trace plan for a running VM from the Kubernetes API: the node
it runs on, its virt-launcher pod and the VM's addresses. It changes nothing
and sends no traffic.

netdoctor never deploys privileged components. To record the trace, an
operator runs 'kubectl-netdoctor host trace --plan PLAN' as root on the
node (for example from a node debug session). That loads read-only eBPF
programs on passive kernel tracepoints for the planned duration, filtered to
the launcher pod's network namespace and the VM's addresses, then detaches.
Feed the result to 'diagnose --trace'.`,
		Example: `  kubectl netdoctor trace vm/web-01 -n demo --duration 60s --file web-01-plan.json
  # on the node, as root:
  kubectl-netdoctor host trace --plan web-01-plan.json --file web-01-trace.json
  # back on the workstation:
  kubectl netdoctor diagnose vm/web-01 -n demo --trace web-01-trace.json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args)
		},
	}
	o.addFlags(cmd)
	cmd.Flags().DurationVar(&o.duration, "duration", ebpf.DefaultDuration, fmt.Sprintf("How long the node tracer records (at most %s)", ebpf.MaxDuration))
	cmd.Flags().StringSliceVar(&o.events, "events", ebpf.AllGroups, "Event groups: drops, tcp, interfaces")
	cmd.Flags().StringVarP(&o.file, "file", "f", "", "Write the plan to this file (mode 0600)")
	return cmd
}

func (o *traceOptions) run(ctx context.Context, args []string) error {
	probe := ebpf.Options{Duration: o.duration, Events: o.events, Unfiltered: true}
	if err := probe.Validate(); err != nil {
		return err
	}
	ctx, cancel := o.withTimeout(ctx)
	defer cancel()
	topo, err := o.discover(ctx, args)
	if err != nil {
		return err
	}
	plan, err := traceplan.Build(topo, probe.Events, probe.Duration, o.now())
	if err != nil {
		return err
	}
	if o.file != "" {
		if err := writeJSONFile(o.file, plan); err != nil {
			return err
		}
	}
	if o.output == output.FormatJSON {
		return output.WriteJSON(o.streams.Out, plan)
	}
	return writePlanText(o.streams.Out, plan, o.file)
}

func writePlanText(w io.Writer, p *traceplan.Plan, file string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Trace plan for %s %s/%s\n", p.Target.Kind, p.Target.Namespace, p.Target.Name)
	fmt.Fprintf(&b, "  node:          %s\n", p.NodeName)
	fmt.Fprintf(&b, "  launcher pod:  %s (uid %s)\n", p.LauncherPod, p.PodUID)
	fmt.Fprintf(&b, "  events:        %s for %s\n", strings.Join(p.Events, ","), p.Duration)
	if len(p.Addresses) == 0 {
		b.WriteString("  addresses:     none reported\n")
	}
	for _, a := range p.Addresses {
		note := ""
		if a.Ambiguous {
			note = "  [ambiguous: " + a.Reason + "]"
		}
		fmt.Fprintf(&b, "  address:       %s (%s, %s)%s\n", a.IP, a.Network, a.Source, note)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "  note:          %s\n", n)
	}
	planArg := file
	if planArg == "" {
		planArg = "PLAN.json"
		b.WriteString("\nSave the plan with --file (or -o json) to use it on the node.\n")
	}
	fmt.Fprintf(&b, "\nOn node %s, as root (netdoctor does not deploy anything):\n", p.NodeName)
	fmt.Fprintf(&b, "  kubectl-netdoctor host trace --plan %s --file trace.json\n", planArg)
	b.WriteString("Then:\n")
	fmt.Fprintf(&b, "  kubectl netdoctor diagnose %s/%s -n %s --trace trace.json\n", targetPrefix(p.Target.Kind), p.Target.Name, p.Target.Namespace)
	_, err := io.WriteString(w, b.String())
	return err
}

func targetPrefix(kind string) string {
	if kind == "VirtualMachine" {
		return "vm"
	}
	return "vmi"
}

func writeJSONFile(path string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- explicit CLI argument
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if err := output.WriteJSON(f, v); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ---- kubectl-netdoctor host trace (node side, privileged) ----

type hostTraceOptions struct {
	plan          string
	nodeName      string
	allowMismatch bool
	duration      time.Duration
	events        []string
	netns         string
	interfaces    []string
	addresses     []string
	unfiltered    bool
	maxEvents     int
	ringBytes     int
	file          string
}

func newHostTraceCommand(streams genericiooptions.IOStreams, env HostEnv) *cobra.Command {
	o := &hostTraceOptions{}
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Record kernel network events on this node with eBPF (experimental)",
		Long: `Trace loads netdoctor's eBPF programs on passive kernel tracepoints, records
for a bounded time and detaches. It observes only: no packet is modified,
dropped, redirected or sent. It must be started explicitly by an operator,
as root (or with CAP_BPF and CAP_PERFMON), on a kernel with BTF.

Hooks: skb:kfree_skb (drops with kernel drop reason), TCP retransmissions,
resets sent/received and state changes, and per-interface packet counts at
netif_receive_skb / net_dev_queue. Hooks missing from the kernel are skipped
and reported.

Events are filtered in the kernel. With --plan (from 'kubectl netdoctor
trace') the filter is every interface in the VM's virt-launcher pod network
namespace plus the VM's addresses, and each event is attributed:
  DirectlyAttributed  on an interface in the launcher pod's namespace
  Correlated          matched an address that belongs only to the VM
  Ambiguous           matched an address other workloads can share
  Unattributed        node-wide trace (--unfiltered)
Without a plan, give --interface and/or --address. Node-wide tracing needs
--unfiltered.

Memory is bounded by --ring-bytes; events that do not fit are counted as
lost, and kernel counters stay exact. The trace records the measured CPU
time of the BPF programs.`,
		Example: `  kubectl-netdoctor host trace --plan web-01-plan.json --file web-01-trace.json
  kubectl-netdoctor host trace --interface eth0 --address 192.0.2.10 --duration 10s --events drops`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.run(cmd.Context(), streams, env)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.plan, "plan", "", "Trace plan from 'kubectl netdoctor trace'")
	f.StringVar(&o.nodeName, "node-name", "", "Kubernetes node name to record (defaults to the plan's node or the hostname)")
	f.BoolVar(&o.allowMismatch, "allow-node-name-mismatch", false, "Allow a node name that differs from this host's name")
	f.DurationVar(&o.duration, "duration", 0, fmt.Sprintf("Recording time (default: the plan's, or %s; at most %s)", ebpf.DefaultDuration, ebpf.MaxDuration))
	f.StringSliceVar(&o.events, "events", nil, "Event groups: drops, tcp, interfaces (default: the plan's, or all)")
	f.StringVar(&o.netns, "netns", "", "Network namespace path for --interface (default: this process's)")
	f.StringSliceVar(&o.interfaces, "interface", nil, "Interface to filter on (repeatable)")
	f.StringSliceVar(&o.addresses, "address", nil, "IP address to filter on, as source or destination (repeatable)")
	f.BoolVar(&o.unfiltered, "unfiltered", false, "Trace node-wide without a filter; events are unattributed")
	f.IntVar(&o.maxEvents, "max-events", ebpf.DefaultMaxEvents, "Maximum events to record; more are counted, not stored")
	f.IntVar(&o.ringBytes, "ring-bytes", ebpf.DefaultRingBytes, "Kernel ring buffer size in bytes (power of two)")
	f.StringVarP(&o.file, "file", "f", "", "Write the trace to this file (mode 0600) instead of stdout")
	return cmd
}

func (o *hostTraceOptions) run(ctx context.Context, streams genericiooptions.IOStreams, env HostEnv) error {
	opts := ebpf.Options{
		Duration: o.duration, Events: o.events, Unfiltered: o.unfiltered,
		MaxEvents: o.maxEvents, RingBytes: o.ringBytes,
	}
	meta := ebpf.Meta{}
	hostname, _ := env.Hostname()
	var plan *traceplan.Plan
	var launcherNetns uint32
	if o.plan != "" {
		var err error
		if plan, err = traceplan.Load(o.plan); err != nil {
			return err
		}
		if o.unfiltered {
			return errors.New("--unfiltered cannot be combined with --plan")
		}
		if o.nodeName == "" {
			o.nodeName = plan.NodeName
		}
		if !host.NamesAgree(plan.NodeName, o.nodeName) {
			return fmt.Errorf("the plan is for node %q, not %q", plan.NodeName, o.nodeName)
		}
		if opts.Duration == 0 {
			if d, err := time.ParseDuration(plan.Duration); err == nil {
				opts.Duration = d
			}
		}
		if len(opts.Events) == 0 {
			opts.Events = plan.Events
		}
		target := plan.Target
		meta.Target = &target
	}
	if o.nodeName == "" {
		o.nodeName = hostname
	}
	if !host.NamesAgree(o.nodeName, hostname) && !o.allowMismatch {
		return fmt.Errorf("node %q does not match this host (%q): run the trace on that node, or pass --allow-node-name-mismatch if the kubelet uses a different node name", o.nodeName, hostname)
	}
	meta.NodeName = o.nodeName

	if plan != nil {
		pn, err := env.Tracer.FindPodNetns(plan.PodUID)
		if err != nil {
			return fmt.Errorf("finding virt-launcher pod %s: %w", plan.LauncherPod, err)
		}
		ifs, err := env.Tracer.ListInterfaces(pn.Path)
		if err != nil {
			return fmt.Errorf("listing interfaces of pod %s: %w", plan.LauncherPod, err)
		}
		launcherNetns = pn.ID
		opts.Interfaces = append(opts.Interfaces, ifs...)
		opts.Addresses = append(opts.Addresses, plan.Filters()...)
	}
	for _, name := range o.interfaces {
		f, err := env.Tracer.ResolveInterface(o.netns, name)
		if err != nil {
			return err
		}
		opts.Interfaces = append(opts.Interfaces, f)
	}
	for _, a := range o.addresses {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return fmt.Errorf("invalid --address %q", a)
		}
		opts.Addresses = append(opts.Addresses, ip)
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	// Ctrl-C ends the trace early but still writes what was recorded.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, _ = fmt.Fprintf(streams.ErrOut, "tracing %s for %s on %d interface(s) and %d address(es)...\n",
		strings.Join(opts.Events, ","), opts.Duration, len(opts.Interfaces), len(opts.Addresses))
	tr, err := env.Tracer.Run(ctx, opts, meta)
	if err != nil {
		return err
	}
	if plan != nil {
		tr.LauncherNetns = launcherNetns
		traceplan.Attribute(tr, launcherNetns, plan.Addresses)
	} else if o.unfiltered {
		traceplan.Attribute(tr, 0, nil)
	}

	if o.file != "" {
		if err := writeJSONFile(o.file, tr); err != nil {
			return err
		}
	} else if err := output.WriteJSON(streams.Out, tr); err != nil {
		return err
	}
	c := tr.Counters
	_, _ = fmt.Fprintf(streams.ErrOut, "recorded %d event(s): %d drop(s), %d retransmit(s), %d reset(s) sent, %d received; %d lost, %d over limit; BPF CPU time %.4f%% of one core\n",
		len(tr.Events), c.Drops, c.TCPRetransmits, c.TCPResetsSent, c.TCPResetsRecv, c.LostEvents, c.SuppressedEvents, tr.Overhead.CPUPercentOne)
	for _, l := range tr.Limitations {
		_, _ = fmt.Fprintf(streams.ErrOut, "note: %s\n", l)
	}
	return nil
}
