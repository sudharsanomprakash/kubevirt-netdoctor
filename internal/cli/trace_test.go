package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/traceplan"
)

const fixturePodUID = "1a6d1f0e-0000-4000-8000-000000000003"

func TestTracePlanText(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	out, err := run(t, clients, "trace", "vm/web-04", "-n", "demo", "--duration", "45s")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "trace-plan.txt", out)
}

func TestTracePlanJSON(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	file := filepath.Join(t.TempDir(), "plan.json")
	out, err := run(t, clients, "trace", "vm/web-04", "-n", "demo", "-o", "json", "--events", "drops", "--file", file)
	if err != nil {
		t.Fatal(err)
	}
	var p traceplan.Plan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.PodUID != fixturePodUID || p.NodeName != "worker-3" || len(p.Events) != 1 || p.Duration != "30s" {
		t.Errorf("plan %+v", p)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("plan file: %v %v", fi, err)
	}
}

func TestTracePlanErrors(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("stopped-invalid-nad.yaml")})
	if _, err := run(t, clients, "trace", "vm/batch-04", "-n", "demo"); err == nil || !strings.Contains(err.Error(), "no running VirtualMachineInstance") {
		t.Errorf("tracing a stopped VM: err = %v", err)
	}
	clients = fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	for _, args := range [][]string{
		{"trace", "vm/web-04", "-n", "demo", "--duration", "1h"},
		{"trace", "vm/web-04", "-n", "demo", "--events", "dns"},
	} {
		if _, err := run(t, clients, args...); err == nil {
			t.Errorf("%v: want error", args)
		}
	}
}

// fakeTracer records the options it was called with.
type fakeTracer struct {
	opts ebpf.Options
	meta ebpf.Meta
}

func (f *fakeTracer) env() HostEnv {
	return HostEnv{
		Hostname: func() (string, error) { return "worker-3", nil },
		Tracer: TracerEnv{
			Run: func(_ context.Context, o ebpf.Options, m ebpf.Meta) (*ebpf.Trace, error) {
				f.opts, f.meta = o, m
				return &ebpf.Trace{
					SchemaVersion: ebpf.SchemaVersion, NodeName: m.NodeName, Target: m.Target,
					StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:00:30Z",
					Events: []ebpf.Event{
						{Type: ebpf.TypeDrop, NetnsID: 99, Interface: "eth0", MatchedBy: []string{"interface"}, DropReason: "NO_SOCKET"},
						{Type: ebpf.TypeTCPRetransmit, Src: "10.244.3.40", Dst: "198.51.100.9", MatchedBy: []string{"address"}},
					},
					Limitations: []string{},
				}, nil
			},
			FindPodNetns: func(uid string) (*ebpf.PodNetns, error) {
				if uid != fixturePodUID {
					return nil, errors.New("unknown pod")
				}
				return &ebpf.PodNetns{Path: "/proc/1234/ns/net", ID: 99, PIDs: 3}, nil
			},
			ListInterfaces: func(string) ([]ebpf.InterfaceFilter, error) {
				return []ebpf.InterfaceFilter{{Name: "eth0", Index: 2, NetnsID: 99}, {Name: "tap0", Index: 3, NetnsID: 99}}, nil
			},
			ResolveInterface: func(_, name string) (ebpf.InterfaceFilter, error) {
				return ebpf.InterfaceFilter{Name: name, Index: 5, NetnsID: 1}, nil
			},
		},
	}
}

func runHostEnv(t *testing.T, env HostEnv, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	streams := genericiooptions.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
	cmd := newRootCommand(streams, func(genericclioptions.RESTClientGetter) (*kube.Clients, error) {
		return nil, errors.New("host commands must not create API clients")
	}, env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func writePlan(t *testing.T) string {
	t.Helper()
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, err := run(t, clients, "trace", "vm/web-04", "-n", "demo", "--duration", "20s", "--file", file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestHostTraceWithPlan(t *testing.T) {
	plan := writePlan(t)
	ft := &fakeTracer{}
	traceFile := filepath.Join(t.TempDir(), "trace.json")
	_, stderr, err := runHostEnv(t, ft.env(), "host", "trace", "--plan", plan, "--file", traceFile)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if ft.opts.Duration != 20*time.Second || len(ft.opts.Interfaces) != 2 || len(ft.opts.Addresses) != 1 || ft.opts.Addresses[0].String() != "10.244.3.40" {
		t.Errorf("tracer options %+v", ft.opts)
	}
	if ft.meta.NodeName != "worker-3" || ft.meta.Target == nil || ft.meta.Target.PodUID != fixturePodUID {
		t.Errorf("meta %+v", ft.meta)
	}
	tr, err := traceplan.LoadTrace(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	if tr.LauncherNetns != 99 || tr.Events[0].Attribution != traceplan.DirectlyAttributed || tr.Events[1].Attribution != traceplan.Correlated {
		t.Errorf("attribution: netns=%d events=%+v", tr.LauncherNetns, tr.Events)
	}
	if !strings.Contains(stderr, "recorded 2 event(s)") {
		t.Errorf("stderr summary: %s", stderr)
	}

	// diagnose consumes the trace.
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	out, err := run(t, clients, "diagnose", "vm/web-04", "-n", "demo", "--trace", traceFile, "-o", "text")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "diagnose-trace.txt", out)
}

func TestHostTraceErrors(t *testing.T) {
	plan := writePlan(t)
	ft := &fakeTracer{}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"host", "trace"}, "a filter is required"},
		{[]string{"host", "trace", "--plan", plan, "--unfiltered"}, "cannot be combined"},
		{[]string{"host", "trace", "--plan", plan, "--node-name", "worker-9"}, "the plan is for node"},
		{[]string{"host", "trace", "--address", "nope"}, "invalid --address"},
		{[]string{"host", "trace", "--interface", "eth0", "--node-name", "other"}, "does not match this host"},
	}
	for _, c := range cases {
		_, _, err := runHostEnv(t, ft.env(), c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}
	// Without a plan, explicit filters are passed through.
	if _, _, err := runHostEnv(t, ft.env(), "host", "trace", "--interface", "eth0", "--address", "192.0.2.1", "--duration", "5s"); err != nil {
		t.Fatal(err)
	}
	if len(ft.opts.Interfaces) != 1 || ft.opts.Interfaces[0].Name != "eth0" || ft.meta.Target != nil {
		t.Errorf("explicit filter %+v %+v", ft.opts, ft.meta)
	}
}

func TestDiagnoseTraceMismatch(t *testing.T) {
	tr := ebpf.Trace{SchemaVersion: ebpf.SchemaVersion, NodeName: "worker-3", StartedAt: "a", EndedAt: "b",
		Target: &ebpf.Target{PodUID: "some-older-pod"}, Events: []ebpf.Event{}}
	file := filepath.Join(t.TempDir(), "trace.json")
	b, _ := json.Marshal(tr)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	out, err := run(t, clients, "diagnose", "vm/web-04", "-n", "demo", "--trace", file, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TRACE_TARGET_MISMATCH") || !strings.Contains(out, `"used": false`) {
		t.Errorf("mismatch not reported:\n%s", out)
	}
	if err := os.WriteFile(file, []byte(`{"schemaVersion":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, clients, "diagnose", "vm/web-04", "-n", "demo", "--trace", file); err == nil {
		t.Error("wrong schema must be rejected")
	}
}
