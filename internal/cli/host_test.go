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

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// stubRunner reports every tool except iproute2 as missing and returns
// empty JSON lists for iproute2.
type stubRunner struct{}

func (stubRunner) LookPath(name string) (string, error) {
	if name == "ip" || name == "bridge" {
		return "/usr/sbin/" + name, nil
	}
	return "", errors.New("not found")
}

func (stubRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[len(args)-1] == "list" {
		return nil, nil // ip -j netns list prints nothing without namespaces
	}
	return []byte("[]"), nil
}

func runHost(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	streams := genericiooptions.IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut}
	env := HostEnv{Runner: stubRunner{}, ProcRoot: t.TempDir(), Hostname: func() (string, error) { return "node-a", nil }}
	cmd := newRootCommand(streams, func(genericclioptions.RESTClientGetter) (*kube.Clients, error) {
		return nil, errors.New("host collect must not create API clients")
	}, env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestHostCollectToStdout(t *testing.T) {
	out, _, err := runHost(t, "host", "collect")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := host.DecodeSnapshot([]byte(out))
	if err != nil {
		t.Fatalf("output is not a valid snapshot: %v\n%s", err, out)
	}
	if snap.NodeName != "node-a" || snap.Hostname != "node-a" {
		t.Errorf("node name defaults to hostname: %+v", snap)
	}
	if src, _ := snap.Source(host.SourceOVSDB); src.Status != host.StatusNotInstalled {
		t.Errorf("ovsdb = %+v", src)
	}
}

func TestHostCollectToFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap.json")
	// An FQDN node name agrees with the short hostname.
	out, _, err := runHost(t, "host", "collect", "--node-name", "node-a.example.internal", "--file", file, "--skip-ovs")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("stdout must be empty when writing to a file, got %q", out)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot file mode = %v, want 0600", info.Mode().Perm())
	}
	snap, err := host.LoadSnapshot(file)
	if err != nil || snap.NodeName != "node-a.example.internal" {
		t.Errorf("snapshot: %+v %v", snap, err)
	}
	if src, _ := snap.Source(host.SourceOVSDB); src.Status != host.StatusSkipped {
		t.Errorf("--skip-ovs: %+v", src)
	}
}

// Collecting on one host while labeling the snapshot with another node is a
// real operator mistake; it is refused unless explicitly allowed.
func TestHostCollectRefusesMismatchedNodeName(t *testing.T) {
	_, _, err := runHost(t, "host", "collect", "--node-name", "worker-2.example.internal")
	if err == nil || !strings.Contains(err.Error(), "does not match this host") {
		t.Fatalf("expected refusal, got %v", err)
	}
	out, _, err := runHost(t, "host", "collect", "--node-name", "worker-2", "--allow-node-name-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := host.DecodeSnapshot([]byte(out))
	if err != nil || snap.NodeName != "worker-2" || snap.Hostname != "node-a" {
		t.Errorf("override: %+v %v", snap, err)
	}
}

func TestHostDiff(t *testing.T) {
	dir := t.TempDir()
	before := host.Snapshot{
		SchemaVersion: host.SchemaVersion,
		NodeName:      "worker-1",
		Interfaces:    []host.Interface{{Name: "eth0", MTU: 1500, AdminUp: true}},
	}
	after := host.Snapshot{
		SchemaVersion: host.SchemaVersion,
		NodeName:      "worker-1",
		Interfaces:    []host.Interface{{Name: "eth0", MTU: 9000, AdminUp: true}},
	}
	write := func(name string, snap host.Snapshot) string {
		path := filepath.Join(dir, name)
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a, b := write("before.json", before), write("after.json", after)

	out, _, err := runHost(t, "host", "diff", a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "interface eth0") || !strings.Contains(out, "mtu: 1500 -> 9000") {
		t.Errorf("diff output = %q", out)
	}

	other := host.Snapshot{SchemaVersion: host.SchemaVersion, NodeName: "worker-2"}
	c := write("other.json", other)
	if _, _, err := runHost(t, "host", "diff", a, c); err == nil || !strings.Contains(err.Error(), "different nodes") {
		t.Errorf("different nodes err = %v", err)
	}
	if _, _, err := runHost(t, "host", "diff", a, c, "--allow-node-name-mismatch"); err != nil {
		t.Errorf("allowed mismatch err = %v", err)
	}

	jsonOut, _, err := runHost(t, "host", "diff", a, b, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, `"changes"`) || !strings.Contains(jsonOut, `"field": "mtu"`) {
		t.Errorf("json diff = %q", jsonOut)
	}
}
