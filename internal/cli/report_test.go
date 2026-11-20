package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/bundle"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
)

func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		_, name, _ := strings.Cut(h.Name, "/")
		out[name] = string(b)
	}
}

func TestReportJSON(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	clients.Core.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", allowExcept())
	out, err := run(t, clients, "report", "vm/web-04", "-n", "demo", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r FullReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "FullReport" || r.Diagnosis == nil || r.Topology == nil || r.Topology.Graph == nil || r.Capabilities == nil {
		t.Errorf("incomplete report: %+v", r)
	}
}

func TestReportBundleRedacted(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("ovs-vlan-running.yaml")})
	clients.Core.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", allowExcept())
	dir := t.TempDir()
	path := filepath.Join(dir, "db-03.tar.gz")
	snap := snapshotFile(t, "worker-2-ovs.json", nil)
	out, err := run(t, clients, "report", "vm/db-03", "-n", "demo", "--host-snapshot", snap, "--bundle", path, "--redact")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("bundle file: %v %v", fi, err)
	}
	files := readBundle(t, path)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"capabilities.json", "diagnosis.json", "diagnosis.txt", "inputs/host-snapshot.json", "manifest.json",
		"topology.dot", "topology.json", "topology.mmd", "topology.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("bundle files %v, want %v", names, want)
	}
	var m bundle.Manifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if !m.Redacted || m.Target != "VirtualMachine demo/db-03" || len(m.Files) != len(want)-1 {
		t.Errorf("manifest %+v", m)
	}
	// Every address from the fixture and snapshot is gone, in the bundle
	// and in the printed report, and placeholders agree between them.
	raw, _ := os.ReadFile(fixture("ovs-vlan-running.yaml"))
	rawSnap, _ := os.ReadFile(snap)
	for _, secret := range []string{"10.244.2.33", "02:00:00:00:c8:03"} {
		if !strings.Contains(string(raw)+string(rawSnap), secret) {
			t.Fatalf("test assumption: %s is in the inputs", secret)
		}
		for name, content := range files {
			if strings.Contains(strings.ToLower(content), secret) {
				t.Errorf("%s still contains %s", name, secret)
			}
		}
		if strings.Contains(out, secret) {
			t.Errorf("printed report still contains %s", secret)
		}
	}
	if !strings.Contains(out, "redacted") || !strings.Contains(files["diagnosis.txt"], "redacted") {
		t.Error("placeholders missing")
	}
	for _, name := range []string{"diagnosis.json", "topology.json", "capabilities.json", "inputs/host-snapshot.json"} {
		if !json.Valid([]byte(files[name])) {
			t.Errorf("%s is not valid JSON after redaction", name)
		}
	}
}

func TestReportErrors(t *testing.T) {
	clients := fakecluster.New(t, []string{fixture("pod-network-only.yaml")})
	if _, err := run(t, clients, "report", "vm/web-04", "-n", "demo", "--bundle", filepath.Join(t.TempDir(), "x.zip")); err == nil {
		t.Error("non-.tar.gz bundle path must be rejected")
	}
	broken := fakecluster.New(t, []string{fixture("stopped-invalid-nad.yaml")})
	if _, err := run(t, broken, "report", "vm/batch-04", "-n", "demo", "--fail-on", "error"); ExitCode(err) != ExitFindings {
		t.Errorf("--fail-on: %v", err)
	}
}
