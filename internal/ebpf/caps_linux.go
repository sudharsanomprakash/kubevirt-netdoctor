//go:build linux

package ebpf

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

// Capability bits (include/uapi/linux/capability.h).
const (
	capNetAdmin = 12
	capSysAdmin = 21
	capPerfmon  = 38
	capBPF      = 39
)

// ProbeCapabilities checks this node. With load set, it loads every BPF
// program through the verifier (without attaching anything) to prove the
// tracer works on this kernel.
func ProbeCapabilities(load bool) NodeCapabilities {
	nc := NodeCapabilities{Arch: runtime.GOARCH, Tracing: CapUnavailable}
	var uts unix.Utsname
	if unix.Uname(&uts) == nil {
		nc.Kernel = unix.ByteSliceToString(uts.Release[:])
	}
	add := func(name, status, detail, impact string) {
		nc.Checks = append(nc.Checks, Capability{Name: name, Status: status, Detail: detail, Impact: impact})
	}

	kspec, err := btf.LoadKernelSpec()
	if err != nil {
		add("kernel BTF", CapUnavailable, err.Error(), "eBPF tracing needs CONFIG_DEBUG_INFO_BTF (/sys/kernel/btf/vmlinux)")
	} else {
		add("kernel BTF", CapAvailable, "/sys/kernel/btf/vmlinux", "")
	}

	eff, err := effectiveCaps()
	switch {
	case err != nil:
		add("privileges", CapNotChecked, err.Error(), "")
	case (eff&(1<<capBPF) != 0 && eff&(1<<capPerfmon) != 0) || eff&(1<<capSysAdmin) != 0:
		add("privileges", CapAvailable, "CAP_BPF and CAP_PERFMON (or CAP_SYS_ADMIN) are effective", "")
	default:
		add("privileges", CapUnavailable, fmt.Sprintf("effective capabilities %#x lack CAP_BPF+CAP_PERFMON and CAP_SYS_ADMIN", eff),
			"run 'host trace' as root or grant CAP_BPF and CAP_PERFMON")
	}
	if eff&(1<<capSysAdmin) == 0 && err == nil {
		add("overhead measurement", CapUnavailable, "BPF run-time statistics need CAP_SYS_ADMIN", "traces will not report measured overhead")
	}
	if eff&(1<<capNetAdmin) == 0 && err == nil {
		add("CAP_NET_ADMIN", CapUnavailable, "", "OVS state in 'host collect' may be unreadable")
	}

	if v, err := os.ReadFile("/proc/sys/kernel/kptr_restrict"); err == nil {
		s := strings.TrimSpace(string(v))
		if k, err := loadKallsyms("/proc/kallsyms"); err == nil && k != nil {
			add("drop locations", CapAvailable, "kernel symbols readable (kptr_restrict="+s+")", "")
		} else {
			add("drop locations", CapDegraded, "kernel symbol addresses hidden (kptr_restrict="+s+")", "drop locations are reported as raw addresses")
		}
	}

	if kspec != nil {
		var en *btf.Enum
		if kspec.TypeByName("skb_drop_reason", &en) == nil {
			add("drop reasons", CapAvailable, fmt.Sprintf("%d reasons in enum skb_drop_reason", len(en.Values)), "")
		} else {
			add("drop reasons", CapUnavailable, "kernel has no enum skb_drop_reason (before 5.17)", "drops are reported without a reason")
		}
	}

	spec, err := loadNetdoctor()
	if err != nil {
		add("BPF objects", CapUnavailable, err.Error(), "")
		return nc
	}
	names := make([]string, 0, len(spec.Programs))
	for n := range spec.Programs {
		names = append(names, n)
	}
	sort.Strings(names)
	present := 0
	for _, n := range names {
		hook := spec.Programs[n].AttachTo
		if kspec == nil {
			add("tracepoint "+hook, CapNotChecked, "", "")
			continue
		}
		var tp *btf.Typedef
		if kspec.TypeByName("btf_trace_"+hook, &tp) != nil {
			add("tracepoint "+hook, CapUnavailable, "not in kernel BTF", programGroups[n]+" events from it are not observed")
			delete(spec.Programs, n)
			continue
		}
		present++
		add("tracepoint "+hook, CapAvailable, "", "")
	}
	if !load {
		add("verifier load", CapNotChecked, "skipped (--load=false)", "")
		if present > 0 {
			nc.Tracing = CapNotChecked
		}
		return nc
	}
	if kspec == nil || present == 0 {
		return nc
	}
	_ = rlimit.RemoveMemlock() // see Run
	spec.Maps["events"].MaxEntries = MinRingBytes
	coll, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{Programs: ebpf.ProgramOptions{KernelTypes: kspec}})
	if err != nil {
		detail := err.Error()
		if errors.Is(err, unix.EPERM) {
			detail = "not permitted: " + detail
		}
		add("verifier load", CapUnavailable, firstLine(detail), "the kernel rejected the tracer")
		return nc
	}
	coll.Close()
	add("verifier load", CapAvailable, fmt.Sprintf("%d program(s) accepted by the verifier; nothing was attached", present), "")
	if present == len(names) {
		nc.Tracing = CapAvailable
	} else {
		nc.Tracing = CapDegraded
	}
	return nc
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func effectiveCaps() (uint64, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	return 0, errors.New("CapEff not found in /proc/self/status")
}
