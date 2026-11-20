package diagnose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/traceplan"
)

// Trace finding IDs.
const (
	IDTraceTargetMismatch  = "TRACE_TARGET_MISMATCH"
	IDTraceDrops           = "TRACE_DROPS"
	IDTraceRetransmits     = "TRACE_TCP_RETRANSMITS"
	IDTraceResets          = "TRACE_TCP_RESETS"
	IDTraceAmbiguous       = "TRACE_AMBIGUOUS_EVENTS"
	IDTraceEventsLost      = "TRACE_EVENTS_LOST"
	IDTraceHookUnavailable = "TRACE_HOOK_UNAVAILABLE"
	IDTraceQuiet           = "TRACE_NO_EVENTS"
)

// TraceRules returns the rules that interpret a kernel trace.
func TraceRules() []Rule {
	return []Rule{{ID: "trace", Description: "Kernel trace events attributed to the VM", Check: checkTrace}}
}

func traceResource(tr *ebpf.Trace) string {
	return fmt.Sprintf("Trace node/%s %s", dash(tr.NodeName), tr.StartedAt)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// TraceMatches reports whether the trace was taken for the topology's
// current virt-launcher pod.
func TraceMatches(in *Input) (bool, string) {
	tr, t := in.Trace, in.Topology
	if tr.Target == nil || tr.Target.PodUID == "" {
		return false, "the trace was not taken with a trace plan, so it is not tied to any pod"
	}
	if t.Launcher == nil || t.Launcher.Selected == nil {
		return false, "no virt-launcher pod is selected for the VMI"
	}
	if tr.Target.PodUID != t.Launcher.Selected.UID {
		return false, fmt.Sprintf("the trace was taken for pod UID %s, but the current virt-launcher pod %s has UID %s (the VM restarted or migrated)",
			tr.Target.PodUID, t.Launcher.Selected.Name, t.Launcher.Selected.UID)
	}
	return true, ""
}

func checkTrace(in *Input) []Finding {
	tr := in.Trace
	if tr == nil {
		return nil
	}
	res := traceResource(tr)
	window := fmt.Sprintf("%s to %s on node %s", tr.StartedAt, tr.EndedAt, dash(tr.NodeName))
	if ok, why := TraceMatches(in); !ok {
		return []Finding{{
			ID: IDTraceTargetMismatch, Severity: SeverityWarning, Category: Confirmed, Resource: res,
			Summary:    "The kernel trace does not belong to this VM's current virt-launcher pod and was not used",
			Evidence:   []string{why},
			NextAction: "Create a new plan with 'kubectl netdoctor trace' and run 'host trace --plan' on the VM's node",
		}}
	}
	var out []Finding
	s := traceplan.Summarize(tr)
	direct := s.ByAttribution[traceplan.DirectlyAttributed]

	category := func(types ...string) Category {
		for _, e := range tr.Events {
			if e.Attribution != traceplan.DirectlyAttributed {
				continue
			}
			for _, t := range types {
				if e.Type == t {
					return Confirmed
				}
			}
		}
		return Suspected
	}

	if n := sumMap(s.DropReasons); n > 0 {
		ev := []string{fmt.Sprintf("%d packet drop(s) attributed to the VM during %s", n, window)}
		for _, k := range keysByCount(s.DropReasons) {
			ev = append(ev, fmt.Sprintf("reason %s: %d", k, s.DropReasons[k]))
		}
		ev = append(ev, examples(tr, ebpf.TypeDrop, 3)...)
		out = append(out, Finding{
			ID: IDTraceDrops, Severity: SeverityWarning, Category: category(ebpf.TypeDrop), Resource: res,
			Summary:  fmt.Sprintf("The kernel dropped %d packet(s) of the VM's traffic", n),
			Evidence: ev,
			NextAction: "Check the drop reasons: NO_SOCKET means nothing listened on the destination port, NETFILTER_DROP a firewall rule, " +
				"OTHERHOST/PKT_TOO_BIG addressing or MTU problems. Some drops are normal (port scans, closed ports)",
		})
	}
	if s.Retransmits > 0 {
		out = append(out, Finding{
			ID: IDTraceRetransmits, Severity: SeverityWarning, Category: category(ebpf.TypeTCPRetransmit), Resource: res,
			Summary:    fmt.Sprintf("%d TCP retransmission(s) on the VM's connections", s.Retransmits),
			Evidence:   append([]string{"observed during " + window}, examples(tr, ebpf.TypeTCPRetransmit, 3)...),
			NextAction: "Retransmissions mean segments or their ACKs were lost or delayed; look for drops, MTU mismatches or link problems on the path to the peers listed",
		})
	}
	if s.ResetsSent+s.ResetsRecv > 0 {
		ev := []string{fmt.Sprintf("%d reset(s) sent and %d received during %s", s.ResetsSent, s.ResetsRecv, window)}
		ev = append(ev, examples(tr, ebpf.TypeTCPResetSent, 2)...)
		ev = append(ev, examples(tr, ebpf.TypeTCPResetRecv, 2)...)
		out = append(out, Finding{
			ID: IDTraceResets, Severity: SeverityInfo, Category: category(ebpf.TypeTCPResetSent, ebpf.TypeTCPResetRecv), Resource: res,
			Summary:    "TCP connections of the VM were reset",
			Evidence:   ev,
			NextAction: "A reset in reply to a connection attempt usually means nothing listens on that port; resets on established connections point to the application or a middlebox",
		})
	}
	if s.AmbiguousOnly > 0 {
		out = append(out, Finding{
			ID: IDTraceAmbiguous, Severity: SeverityInfo, Category: Suspected, Resource: res,
			Summary:  fmt.Sprintf("%d event(s) matched only an address other workloads can share and are not counted for the VM", s.AmbiguousOnly),
			Evidence: []string{"for example masquerade guest addresses or link-local addresses"},
		})
	}
	if tr.Counters.LostEvents > 0 || tr.Counters.SuppressedEvents > 0 {
		out = append(out, Finding{
			ID: IDTraceEventsLost, Severity: SeverityInfo, Category: Unavailable, Resource: res,
			Summary: "Some trace events were not recorded; event counts above are lower bounds",
			Evidence: []string{
				fmt.Sprintf("%d lost (ring buffer full), %d beyond the event limit", tr.Counters.LostEvents, tr.Counters.SuppressedEvents),
				fmt.Sprintf("kernel counters for the whole filter: %d drops, %d retransmits, %d resets sent, %d received",
					tr.Counters.Drops, tr.Counters.TCPRetransmits, tr.Counters.TCPResetsSent, tr.Counters.TCPResetsRecv),
			},
			NextAction: "Re-run with a larger --ring-bytes or --max-events, or fewer --events groups",
		})
	}
	var missing []string
	for _, p := range tr.Programs {
		if !p.Attached && p.Error != "" && p.Error != "not requested" {
			missing = append(missing, fmt.Sprintf("%s: %s", p.Hook, p.Error))
		}
	}
	if len(missing) > 0 {
		out = append(out, Finding{
			ID: IDTraceHookUnavailable, Severity: SeverityInfo, Category: Unavailable, Resource: res,
			Summary:  "Some kernel hooks could not be used; their events were not observed",
			Evidence: missing,
		})
	}
	if sumMap(s.DropReasons)+s.Retransmits+s.ResetsSent+s.ResetsRecv == 0 {
		out = append(out, Finding{
			ID: IDTraceQuiet, Severity: SeverityInfo, Category: Confirmed, Resource: res,
			Summary:  "No drops, retransmissions or resets were attributed to the VM during the trace",
			Evidence: []string{"window " + window, fmt.Sprintf("%d event(s) recorded in total, %d directly attributed", len(tr.Events), direct)},
			Expected: "This covers only the trace window and the hooks that were attached; it does not show that connectivity works",
		})
	}
	return out
}

func examples(tr *ebpf.Trace, typ string, n int) []string {
	var out []string
	for _, e := range tr.Events {
		if e.Type != typ || (e.Attribution != traceplan.DirectlyAttributed && e.Attribution != traceplan.Correlated) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s", e.Time, e.Type)
		if e.Protocol != "" {
			fmt.Fprintf(&b, " %s", e.Protocol)
		}
		if e.Src != "" || e.Dst != "" {
			fmt.Fprintf(&b, " %s:%d -> %s:%d", e.Src, e.SrcPort, e.Dst, e.DstPort)
		}
		if e.Interface != "" {
			fmt.Fprintf(&b, " on %s", e.Interface)
		}
		if e.DropReason != "" {
			fmt.Fprintf(&b, " reason=%s", e.DropReason)
		}
		if e.Location != "" {
			fmt.Fprintf(&b, " at %s", e.Location)
		}
		fmt.Fprintf(&b, " [%s]", e.Attribution)
		out = append(out, strings.TrimSpace(b.String()))
		if len(out) == n {
			break
		}
	}
	return out
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func keysByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}
