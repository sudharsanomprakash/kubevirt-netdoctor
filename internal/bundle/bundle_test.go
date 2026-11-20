package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRedactor(t *testing.T) {
	r := NewRedactor()
	in := `{"ip":"10.244.3.40","cidr":"10.244.3.0/24","mac":"0A:58:0a:f4:03:28","v6":"fd00::1","ll":"fe80::1%eth0",` +
		`"doc":"192.0.2.10","lo":"127.0.0.1","time":"2026-11-20T05:32:09Z","ver":"v1.2.3","again":"10.244.3.40","mac2":"0a:58:0a:f4:03:28"}`
	out := string(r.Redact([]byte(in)))
	for _, gone := range []string{"10.244.3.40", "10.244.3.0", "0A:58", "0a:58", "fd00::1", "fe80::1"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q not redacted: %s", gone, out)
		}
	}
	for _, kept := range []string{"192.0.2.10", "127.0.0.1", "2026-11-20T05:32:09Z", "v1.2.3", "/24", "%eth0"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q must be kept: %s", kept, out)
		}
	}
	if strings.Count(out, "ip4-redacted-1") != 2 || strings.Count(out, "mac-1") != 2 {
		t.Errorf("replacement must be stable: %s", out)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("redaction broke JSON: %s", out)
	}
}

func TestBundleArchive(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	build := func() []byte {
		b := New("nd-report", "v0.0.0-test", "VirtualMachine demo/web", created, NewRedactor())
		if err := b.Add("diagnosis.txt", "text report", []byte("pod ip 10.1.2.3\n")); err != nil {
			t.Fatal(err)
		}
		if err := b.AddJSON("a.json", "json", map[string]string{"ip": "10.1.2.3"}); err != nil {
			t.Fatal(err)
		}
		if err := b.Add("a.json", "dup", nil); err == nil {
			t.Error("duplicate entries must be rejected")
		}
		var buf bytes.Buffer
		if err := b.Write(&buf); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	data := build()
	if !bytes.Equal(data, build()) {
		t.Error("the same inputs must produce the same archive")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Mode != 0o600 || !strings.HasPrefix(h.Name, "nd-report/") {
			t.Errorf("entry %s mode %o", h.Name, h.Mode)
		}
		b, _ := io.ReadAll(tr)
		files[strings.TrimPrefix(h.Name, "nd-report/")] = b
		names = append(names, h.Name)
	}
	if names[0] != "nd-report/manifest.json" {
		t.Errorf("manifest must come first: %v", names)
	}
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if !m.Redacted || len(m.Files) != 2 || len(m.Notes) != 1 {
		t.Errorf("manifest %+v", m)
	}
	for _, f := range m.Files {
		sum := sha256.Sum256(files[f.Name])
		if hex.EncodeToString(sum[:]) != f.SHA256 || f.Bytes != len(files[f.Name]) {
			t.Errorf("checksum mismatch for %s", f.Name)
		}
		if strings.Contains(string(files[f.Name]), "10.1.2.3") {
			t.Errorf("%s not redacted", f.Name)
		}
	}
	if !strings.Contains(string(files["diagnosis.txt"]), "ip4-redacted-1") || !strings.Contains(string(files["a.json"]), "ip4-redacted-1") {
		t.Error("placeholders must be consistent across files")
	}
}
