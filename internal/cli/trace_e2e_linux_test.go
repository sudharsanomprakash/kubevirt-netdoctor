//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/traceplan"
)

// The pod-network-only fixture's virt-launcher pod and its IP.
const (
	e2ePodUID = "1a6d1f0e-0000-4000-8000-000000000003"
	e2ePodIP  = "10.244.3.40"
	e2ePeerIP = "10.244.3.1"
)

// TestTraceEndToEnd runs the whole chain on a real kernel:
//
//	trace vm/web-04 (fake cluster) -> plan
//	host trace --plan (real eBPF; a process in its own netns and in a
//	  kubelet-style pod cgroup stands in for virt-launcher)
//	diagnose vm/web-04 --trace
func TestTraceEndToEnd(t *testing.T) {
	if os.Getenv("NETDOCTOR_BPF_TESTS") == "0" || os.Geteuid() != 0 {
		t.Skip("needs root (set NETDOCTOR_BPF_TESTS=0 to skip)")
	}
	for _, p := range []string{"/sys/kernel/btf/vmlinux", "/sys/fs/cgroup/pids"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s not available", p)
		}
	}
	launcherNS, peerNS := setupPodNet(t)
	startFakeLauncher(t, launcherNS)

	dir := t.TempDir()
	planFile, traceFile := filepath.Join(dir, "plan.json"), filepath.Join(dir, "trace.json")
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})

	out, err := run(t, clients, "trace", "vm/web-04", "-n", "demo", "--duration", "3s", "--events", "drops,tcp", "--file", planFile)
	if err != nil {
		t.Fatalf("trace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "host trace --plan "+planFile) {
		t.Errorf("plan text lacks the node command:\n%s", out)
	}

	// Traffic from the peer namespace toward the "launcher" pod, during the
	// trace: UDP to a closed port (drop) and TCP to a closed port (reset).
	stop := make(chan struct{})
	go func() {
		time.Sleep(500 * time.Millisecond)
		for i := 0; i < 8; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = inNS(peerNS, func() error {
				if pc, err := net.ListenPacket("udp4", e2ePeerIP+":0"); err == nil {
					_, _ = pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.ParseIP(e2ePodIP), Port: 9999})
					_ = pc.Close()
				}
				if c, err := net.DialTimeout("tcp4", e2ePodIP+":7", 200*time.Millisecond); err == nil {
					_ = c.Close()
				}
				return nil
			})
			time.Sleep(200 * time.Millisecond)
		}
	}()
	var hout, herr bytes.Buffer
	streams := genericiooptions.IOStreams{In: strings.NewReader(""), Out: &hout, ErrOut: &herr}
	env := DefaultHostEnv()
	env.Hostname = func() (string, error) { return "worker-3", nil }
	cmd := newRootCommand(streams, func(genericclioptions.RESTClientGetter) (*kube.Clients, error) {
		return nil, fmt.Errorf("host trace must not create API clients")
	}, env)
	cmd.SetArgs([]string{"host", "trace", "--plan", planFile, "--file", traceFile})
	err = cmd.Execute()
	close(stop)
	if err != nil {
		t.Fatalf("host trace: %v\n%s", err, herr.String())
	}
	t.Logf("host trace: %s", herr.String())

	tr, err := traceplan.LoadTrace(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Target == nil || tr.Target.PodUID != e2ePodUID || tr.LauncherNetns == 0 {
		t.Fatalf("trace not tied to the launcher pod: target=%+v netns=%d", tr.Target, tr.LauncherNetns)
	}
	var direct, drops, resets int
	for _, e := range tr.Events {
		if e.Attribution == traceplan.DirectlyAttributed {
			direct++
		}
		if e.Type == ebpf.TypeDrop && e.DstPort == 9999 {
			drops++
			if e.Attribution != traceplan.DirectlyAttributed || e.Interface != "eth0" || e.DropReason != "NO_SOCKET" {
				t.Errorf("drop event %+v: want DirectlyAttributed on eth0 with NO_SOCKET", e)
			}
		}
		if e.Type == ebpf.TypeTCPResetSent && e.SrcPort == 7 {
			resets++
			if e.Attribution != traceplan.Correlated && e.Attribution != traceplan.DirectlyAttributed {
				t.Errorf("reset event %+v is not attributed", e)
			}
		}
	}
	if drops == 0 || resets == 0 || direct == 0 {
		t.Fatalf("drops=%d resets=%d direct=%d; events=%d counters=%+v", drops, resets, direct, len(tr.Events), tr.Counters)
	}

	out, err = run(t, clients, "diagnose", "vm/web-04", "-n", "demo", "--trace", traceFile, "-o", "json")
	if err != nil {
		t.Fatalf("diagnose: %v\n%s", err, out)
	}
	var rep struct {
		Findings []struct{ ID, Category string } `json:"findings"`
		Trace    struct {
			Used bool `json:"used"`
		} `json:"trace"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Trace.Used {
		t.Error("diagnose did not use the trace")
	}
	got := map[string]string{}
	for _, f := range rep.Findings {
		got[f.ID] = f.Category
	}
	if got["TRACE_DROPS"] != "Confirmed" {
		t.Errorf("TRACE_DROPS = %q, want Confirmed; findings %v", got["TRACE_DROPS"], got)
	}
	if _, ok := got["TRACE_TCP_RESETS"]; !ok {
		t.Errorf("TRACE_TCP_RESETS missing; findings %v", got)
	}
	text, err := run(t, clients, "diagnose", "vm/web-04", "-n", "demo", "--trace", traceFile)
	if err != nil || !strings.Contains(text, "KERNEL TRACE") {
		t.Errorf("text report lacks the trace section: %v\n%s", err, text)
	}
}

func sh(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func setupPodNet(t *testing.T) (launcher, peer string) {
	suffix := fmt.Sprintf("%d", os.Getpid()%100000)
	l, p := "ndl"+suffix, "ndp"+suffix
	for _, ns := range []string{l, p} {
		sh(t, "ip", "netns", "add", ns)
		ns := ns
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
	}
	sh(t, "ip", "link", "add", "eth0", "netns", l, "type", "veth", "peer", "name", "veth-pod", "netns", p)
	sh(t, "ip", "-n", l, "addr", "add", e2ePodIP+"/24", "dev", "eth0")
	sh(t, "ip", "-n", p, "addr", "add", e2ePeerIP+"/24", "dev", "veth-pod")
	for _, a := range [][]string{{l, "eth0"}, {p, "veth-pod"}, {l, "lo"}, {p, "lo"}} {
		sh(t, "ip", "-n", a[0], "link", "set", a[1], "up")
	}
	return "/run/netns/" + l, "/run/netns/" + p
}

// startFakeLauncher runs a process in the launcher namespace inside a
// kubelet-style (cgroupfs driver) pod cgroup.
func startFakeLauncher(t *testing.T, ns string) {
	cg := filepath.Join("/sys/fs/cgroup/pids/kubepods-netdoctor-test", "pod"+e2ePodUID)
	if err := os.MkdirAll(cg, 0o755); err != nil {
		t.Skipf("cannot create a test cgroup: %v", err)
	}
	cmd := exec.Command("ip", "netns", "exec", filepath.Base(ns), "sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(cg)
		_ = os.Remove(filepath.Dir(cg))
	})
	if err := os.WriteFile(filepath.Join(cg, "cgroup.procs"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Wait until the process has entered the namespace.
	want, _ := ebpf.NetnsID(ns)
	for i := 0; i < 100; i++ {
		if id, err := ebpf.NetnsID(fmt.Sprintf("/proc/%d/ns/net", cmd.Process.Pid)); err == nil && id == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake launcher did not enter its network namespace")
}

func inNS(path string, fn func() error) error {
	runtime.LockOSThread()
	orig, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer orig.Close()
	f, err := os.Open(path)
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer f.Close()
	if err := unix.Setns(int(f.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	ferr := fn()
	if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err != nil {
		return err // leave the thread locked so it is discarded
	}
	runtime.UnlockOSThread()
	return ferr
}
