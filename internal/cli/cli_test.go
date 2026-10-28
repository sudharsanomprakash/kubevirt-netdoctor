package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/inspect"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

// run executes the CLI against a fake cluster built from the fixtures.
func run(t *testing.T, clients *kube.Clients, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	streams := genericiooptions.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
	cmd := NewRootCommand(streams, func(genericclioptions.RESTClientGetter) (*kube.Clients, error) {
		return clients, nil
	})
	// Isolate tests from any kubeconfig on the machine running them.
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--kubeconfig", kubeconfig)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (run go test ./internal/cli -update to accept):\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestInspectGolden(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		opts    []fakecluster.Option
		args    []string
		golden  string
	}{
		{"bridge vlan running vm json", "bridge-vlan-running.yaml", nil,
			[]string{"inspect", "vm/web-01", "-n", "demo", "-o", "json"}, "bridge-vlan-running.json"},
		{"bridge vlan running vm text", "bridge-vlan-running.yaml", nil,
			[]string{"inspect", "vm/web-01", "-n", "demo"}, "bridge-vlan-running.txt"},
		{"ovs default network without guest agent", "ovs-no-guest-agent.yaml", nil,
			[]string{"inspect", "vmi/db-02", "-n", "demo", "-o", "json"}, "ovs-no-guest-agent.json"},
		{"missing nad keeps vmi scheduling", "nad-missing-scheduling.yaml", nil,
			[]string{"inspect", "vm", "app-03", "--namespace", "demo", "-o", "json"}, "nad-missing-scheduling.json"},
		{"missing nad text", "nad-missing-scheduling.yaml", nil,
			[]string{"inspect", "vm/app-03", "-n", "demo"}, "nad-missing-scheduling.txt"},
		{"stopped vm with invalid nad", "stopped-invalid-nad.yaml", nil,
			[]string{"inspect", "vm/batch-04", "-n", "demo", "-o", "json"}, "stopped-invalid-nad.json"},
		{"attachment missing from network-status", "attachment-missing.yaml", nil,
			[]string{"inspect", "vmi/edge-05", "-n", "demo", "-o", "json"}, "attachment-missing.json"},
		{"attachment missing text", "attachment-missing.yaml", nil,
			[]string{"inspect", "vmi/edge-05", "-n", "demo"}, "attachment-missing.txt"},
		{"live migration selects pod on vmi node", "migration-in-progress.yaml", nil,
			[]string{"inspect", "vmi/api-06", "-n", "demo", "-o", "json"}, "migration-in-progress.json"},
		{"rbac denies nads and pods", "bridge-vlan-running.yaml",
			[]fakecluster.Option{fakecluster.Forbid("get", "network-attachment-definitions"), fakecluster.Forbid("list", "pods")},
			[]string{"inspect", "vm/web-01", "-n", "demo", "-o", "json"}, "rbac-denied.json"},
		{"multus api not served", "bridge-vlan-running.yaml", []fakecluster.Option{fakecluster.WithoutNADAPI()},
			[]string{"inspect", "vm/web-01", "-n", "demo", "-o", "json"}, "nad-api-missing.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients := fakecluster.New(t, []string{fixture(tc.fixture)}, tc.opts...)
			out, err := run(t, clients, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.HasSuffix(tc.golden, ".json") && !json.Valid([]byte(out)) {
				t.Fatalf("output is not valid JSON:\n%s", out)
			}
			assertGolden(t, tc.golden, out)
		})
	}
}

func TestInspectTargetErrors(t *testing.T) {
	tests := []struct {
		name      string
		opts      []fakecluster.Option
		args      []string
		wantState kube.LookupState
		wantMsg   string
	}{
		{"vm not found", nil, []string{"inspect", "vm/nope", "-n", "demo"},
			kube.StateNotFound, `VirtualMachine "nope" not found in namespace "demo"`},
		{"vmi not found", nil, []string{"inspect", "vmi/web-01", "-n", "other"},
			kube.StateNotFound, `VirtualMachineInstance "web-01" not found in namespace "other"`},
		{"vm forbidden", []fakecluster.Option{fakecluster.Forbid("get", "virtualmachines")},
			[]string{"inspect", "vm/web-01", "-n", "demo"}, kube.StateForbidden, "not permitted to read VirtualMachine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clients := fakecluster.New(t, []string{fixture("bridge-vlan-running.yaml")}, tc.opts...)
			_, err := run(t, clients, tc.args...)
			var te *inspect.TargetError
			if !errors.As(err, &te) {
				t.Fatalf("expected TargetError, got %v", err)
			}
			if te.State != tc.wantState {
				t.Errorf("state = %s, want %s", te.State, tc.wantState)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tc.wantMsg)
			}
		})
	}
}

func TestInspectArgumentErrors(t *testing.T) {
	clients := fakecluster.New(t, nil)
	for _, args := range [][]string{
		{"inspect", "pod/x"},
		{"inspect", "web-01"},
		{"inspect", "vm/"},
		{"inspect", "vm/web-01", "-o", "yaml"},
	} {
		if _, err := run(t, clients, args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		args     []string
		wantKind string
		wantName string
	}{
		{[]string{"vm/a"}, topology.KindVM, "a"},
		{[]string{"VirtualMachines/a"}, topology.KindVM, "a"},
		{[]string{"virtualmachine.kubevirt.io/a"}, topology.KindVM, "a"},
		{[]string{"vmi/b"}, topology.KindVMI, "b"},
		{[]string{"vmi", "b"}, topology.KindVMI, "b"},
		{[]string{"virtualmachineinstances", "b"}, topology.KindVMI, "b"},
	}
	for _, tc := range tests {
		kind, name, err := parseTarget(tc.args)
		if err != nil || kind != tc.wantKind || name != tc.wantName {
			t.Errorf("parseTarget(%v) = %q, %q, %v; want %q, %q", tc.args, kind, name, err, tc.wantKind, tc.wantName)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, nil, "version", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"version", "commit", "date", "goVersion", "platform"} {
		if v[k] == "" {
			t.Errorf("missing %q in %s", k, out)
		}
	}
	out, err = run(t, nil, "version")
	if err != nil || !strings.HasPrefix(out, "kubectl-netdoctor ") {
		t.Errorf("text version = %q, %v", out, err)
	}
}
