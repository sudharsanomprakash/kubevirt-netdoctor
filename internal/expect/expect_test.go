package expect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	s, err := Parse([]byte(`
networks:
  vlan100:
    vlan: 100
    mtu: 9000
bonds:
  bond0:
    partnerSystemID: "02:aa:00:00:00:01"
    members: [eno1, eno2]
`))
	if err != nil {
		t.Fatal(err)
	}
	n, ok := s.Network("vlan100")
	if !ok || *n.VLAN != 100 || *n.MTU != 9000 {
		t.Errorf("network: %+v", n)
	}
	b, ok := s.Bond("bond0")
	if !ok || b.PartnerSystemID != "02:aa:00:00:00:01" || len(b.Members) != 2 {
		t.Errorf("bond: %+v", b)
	}
	if _, ok := s.Network("other"); ok {
		t.Error("unexpected network")
	}
	var nilSpec *Spec
	if _, ok := nilSpec.Bond("bond0"); ok {
		t.Error("nil spec must have no expectations")
	}
}

func TestParseInvalid(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field":  "networks:\n  a:\n    vlanid: 5\n",
		"vlan range":     "networks:\n  a:\n    vlan: 5000\n",
		"mtu range":      "networks:\n  a:\n    mtu: 10\n",
		"bad partner":    "bonds:\n  b:\n    partnerSystemID: nope\n",
		"top-level typo": "network:\n  a: {}\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exp.yaml")
	if err := os.WriteFile(path, []byte("networks: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "reading expectations") {
		t.Errorf("missing file error = %v", err)
	}
}
