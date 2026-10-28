package multus

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCNIConfigValid(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantList    bool
		wantTypes   []string
		wantSummary string
		wantIPAM    string
		wantWarn    []string
	}{
		{
			name:        "linux bridge single plugin with vlan",
			raw:         `{"cniVersion":"0.3.1","name":"br","type":"bridge","bridge":"br1","vlan":100,"ipam":{"type":"host-local"}}`,
			wantTypes:   []string{"bridge"},
			wantSummary: "bridge(br=br1,vlan=100)",
			wantIPAM:    "host-local",
		},
		{
			name: "linux bridge conflist chained with tuning",
			raw: `{"cniVersion":"0.3.1","name":"br","plugins":[
				{"type":"bridge","bridge":"br1","vlanTrunk":[{"minID":10,"maxID":20}]},{"type":"tuning"}]}`,
			wantList:    true,
			wantTypes:   []string{"bridge", "tuning"},
			wantSummary: "bridge(br=br1,trunk) -> tuning",
		},
		{
			name:        "ovs cni",
			raw:         `{"cniVersion":"0.4.0","name":"ovs","type":"ovs","bridge":"br-data","vlan":200}`,
			wantTypes:   []string{"ovs"},
			wantSummary: "ovs(br=br-data,vlan=200)",
		},
		{
			name:        "ovs cni trunk",
			raw:         `{"cniVersion":"0.4.0","name":"ovs","type":"ovs","bridge":"br-data","trunk":[{"id":42}]}`,
			wantTypes:   []string{"ovs"},
			wantSummary: "ovs(br=br-data,trunk)",
		},
		{
			name:        "macvlan with whereabouts",
			raw:         `{"cniVersion":"0.3.1","name":"m","type":"macvlan","master":"bond0.50","mode":"bridge","ipam":{"type":"whereabouts"}}`,
			wantTypes:   []string{"macvlan"},
			wantSummary: "macvlan(master=bond0.50)",
			wantIPAM:    "whereabouts",
		},
		{
			name:        "ovn-kubernetes localnet",
			raw:         `{"cniVersion":"0.3.1","name":"ln","type":"ovn-k8s-cni-overlay","topology":"localnet","netAttachDefName":"demo/ln"}`,
			wantTypes:   []string{"ovn-k8s-cni-overlay"},
			wantSummary: "ovn-k8s-cni-overlay(topology=localnet)",
		},
		{
			name:        "calico conflist as found on primary networks",
			raw:         `{"name":"k8s-pod-network","cniVersion":"0.3.1","plugins":[{"type":"calico","ipam":{"type":"calico-ipam"}},{"type":"portmap"},{"type":"bandwidth"}]}`,
			wantList:    true,
			wantTypes:   []string{"calico", "portmap", "bandwidth"},
			wantSummary: "calico -> portmap -> bandwidth",
			wantIPAM:    "calico-ipam",
		},
		{
			name:        "unknown plugin type is accepted",
			raw:         `{"cniVersion":"1.0.0","name":"x","type":"vendor-cni"}`,
			wantTypes:   []string{"vendor-cni"},
			wantSummary: "vendor-cni",
		},
		{
			name:        "missing cniVersion warns",
			raw:         `{"name":"br","type":"bridge","bridge":"br1"}`,
			wantTypes:   []string{"bridge"},
			wantSummary: "bridge(br=br1)",
			wantWarn:    []string{"cniVersion is not set"},
		},
		{
			name:      "meta plugin first warns",
			raw:       `{"cniVersion":"0.3.1","name":"x","plugins":[{"type":"tuning"},{"type":"bridge","bridge":"br1"}]}`,
			wantList:  true,
			wantTypes: []string{"tuning", "bridge"},
			wantWarn:  []string{`first plugin "tuning" is a meta plugin`},
		},
		{
			name:      "only meta plugins warns",
			raw:       `{"cniVersion":"0.3.1","name":"x","plugins":[{"type":"tuning"}]}`,
			wantList:  true,
			wantTypes: []string{"tuning"},
			wantWarn:  []string{"no plugin in the chain creates an interface"},
		},
		{
			name:      "type and plugins both set warns",
			raw:       `{"cniVersion":"0.3.1","name":"x","type":"bridge","plugins":[{"type":"bridge"}]}`,
			wantList:  true,
			wantTypes: []string{"bridge"},
			wantWarn:  []string{`both "type" and "plugins" are set`},
		},
		{
			name:      "conflist without name warns",
			raw:       `{"cniVersion":"0.3.1","plugins":[{"type":"bridge"}]}`,
			wantList:  true,
			wantTypes: []string{"bridge"},
			wantWarn:  []string{"configuration list has no name"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseCNIConfig(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.IsList != tc.wantList {
				t.Errorf("IsList = %v, want %v", cfg.IsList, tc.wantList)
			}
			var types []string
			for _, p := range cfg.Plugins {
				types = append(types, p.Type)
			}
			if strings.Join(types, ",") != strings.Join(tc.wantTypes, ",") {
				t.Errorf("plugin types = %v, want %v", types, tc.wantTypes)
			}
			if tc.wantSummary != "" && cfg.Summary() != tc.wantSummary {
				t.Errorf("Summary() = %q, want %q", cfg.Summary(), tc.wantSummary)
			}
			if tc.wantIPAM != "" {
				if p := cfg.Primary(); p == nil || p.IPAM != tc.wantIPAM {
					t.Errorf("primary IPAM = %+v, want %q", p, tc.wantIPAM)
				}
			}
			if len(tc.wantWarn) != len(cfg.Warnings) {
				t.Fatalf("warnings = %q, want %q", cfg.Warnings, tc.wantWarn)
			}
			for i, w := range tc.wantWarn {
				if !strings.Contains(cfg.Warnings[i], w) {
					t.Errorf("warning %d = %q, want it to contain %q", i, cfg.Warnings[i], w)
				}
			}
		})
	}
}

func TestParseCNIConfigInvalid(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"not json", `{"cniVersion": "0.3.1",`, "not a valid JSON object"},
		{"json array", `[{"type":"bridge"}]`, "not a valid JSON object"},
		{"trailing data", `{"type":"bridge"} {"type":"ovs"}`, "unexpected data"},
		{"no type or plugins", `{"cniVersion":"0.3.1","name":"x"}`, `neither "type" nor "plugins"`},
		{"empty plugins", `{"cniVersion":"0.3.1","name":"x","plugins":[]}`, "list is empty"},
		{"plugins not a list", `{"cniVersion":"0.3.1","name":"x","plugins":{"type":"bridge"}}`, "must be a list"},
		{"plugin without type", `{"cniVersion":"0.3.1","name":"x","plugins":[{"bridge":"br1"}]}`, `plugins[0]: plugin has no "type"`},
		{"type not a string", `{"cniVersion":"0.3.1","type":5}`, `"type" must be a string`},
		{"vlan not an integer", `{"cniVersion":"0.3.1","type":"bridge","vlan":"100"}`, `"vlan" must be an integer`},
		{"vlan out of range", `{"cniVersion":"0.3.1","type":"ovs","vlan":4095}`, "outside the valid range"},
		{"ipam not an object", `{"cniVersion":"0.3.1","type":"bridge","ipam":"dhcp"}`, `"ipam" must be an object`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCNIConfig(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseCNIConfigEmpty(t *testing.T) {
	for _, raw := range []string{"", "  \n"} {
		if _, err := ParseCNIConfig(raw); !errors.Is(err, ErrEmptyConfig) {
			t.Errorf("ParseCNIConfig(%q) error = %v, want ErrEmptyConfig", raw, err)
		}
	}
}
