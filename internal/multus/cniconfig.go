package multus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CNIConfig is a parsed NetworkAttachmentDefinition spec.config. Both single
// plugin configurations and plugin lists (conflists) are normalized into an
// ordered plugin chain.
type CNIConfig struct {
	CNIVersion string      `json:"cniVersion,omitempty"`
	Name       string      `json:"name,omitempty"`
	IsList     bool        `json:"isList"`
	Plugins    []CNIPlugin `json:"plugins"`
	// Warnings are suspicious but not necessarily fatal properties.
	Warnings []string `json:"warnings,omitempty"`
}

// CNIPlugin is one plugin in the chain, with the attributes netdoctor knows
// how to interpret for common plugin types. Unknown plugin types are kept
// with their type only.
type CNIPlugin struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	// Role is "interface" for plugins that create the attachment interface,
	// "meta" for chained plugins that modify it, or "unknown".
	Role     string `json:"role"`
	Bridge   string `json:"bridge,omitempty"`
	VLAN     *int   `json:"vlan,omitempty"`
	Trunk    bool   `json:"trunk,omitempty"`
	Master   string `json:"master,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Device   string `json:"device,omitempty"`
	Topology string `json:"topology,omitempty"`
	MTU      *int   `json:"mtu,omitempty"`
	IPAM     string `json:"ipam,omitempty"`
}

// Primary returns the first interface-creating plugin, or nil.
func (c *CNIConfig) Primary() *CNIPlugin {
	for i := range c.Plugins {
		if c.Plugins[i].Role == RoleInterface {
			return &c.Plugins[i]
		}
	}
	return nil
}

// Plugin roles.
const (
	RoleInterface = "interface"
	RoleMeta      = "meta"
	RoleUnknown   = "unknown"
)

type pluginInfo struct {
	description string
	role        string
}

// knownPlugins describes CNI plugin types commonly used with KubeVirt. The
// list is descriptive only; unknown types are accepted.
var knownPlugins = map[string]pluginInfo{
	"bridge":              {"Linux bridge", RoleInterface},
	"cnv-bridge":          {"Linux bridge (cnv-bridge)", RoleInterface},
	"ovs":                 {"Open vSwitch (OVS CNI)", RoleInterface},
	"ovn-k8s-cni-overlay": {"OVN-Kubernetes secondary network", RoleInterface},
	"kube-ovn":            {"Kube-OVN", RoleInterface},
	"calico":              {"Calico", RoleInterface},
	"cilium-cni":          {"Cilium", RoleInterface},
	"flannel":             {"Flannel", RoleInterface},
	"sriov":               {"SR-IOV", RoleInterface},
	"macvlan":             {"macvlan", RoleInterface},
	"ipvlan":              {"ipvlan", RoleInterface},
	"host-device":         {"host device", RoleInterface},
	"ptp":                 {"point-to-point veth", RoleInterface},
	"tuning":              {"tuning (sysctl/MAC/MTU)", RoleMeta},
	"cnv-tuning":          {"tuning (cnv-tuning)", RoleMeta},
	"portmap":             {"port mapping", RoleMeta},
	"bandwidth":           {"bandwidth shaping", RoleMeta},
	"sbr":                 {"source-based routing", RoleMeta},
	"firewall":            {"firewall", RoleMeta},
	"vrf":                 {"VRF", RoleMeta},
}

// ErrEmptyConfig is returned when a NAD has no inline CNI configuration. In
// that case Multus resolves the configuration from a file on each node, which
// netdoctor cannot observe through the Kubernetes API.
var ErrEmptyConfig = errors.New("spec.config is empty; Multus resolves the CNI configuration from a file on the node")

// ParseCNIConfig parses and validates a NAD spec.config string.
//
// Errors indicate configurations that libcni/Multus would reject. Warnings
// flag properties that are valid JSON but likely problematic.
func ParseCNIConfig(raw string) (*CNIConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrEmptyConfig
	}
	var top map[string]json.RawMessage
	if err := strictUnmarshal([]byte(raw), &top); err != nil {
		return nil, fmt.Errorf("spec.config is not a valid JSON object: %w", err)
	}

	cfg := &CNIConfig{}
	if err := optionalString(top, "cniVersion", &cfg.CNIVersion); err != nil {
		return nil, err
	}
	if err := optionalString(top, "name", &cfg.Name); err != nil {
		return nil, err
	}
	if cfg.CNIVersion == "" {
		cfg.Warnings = append(cfg.Warnings, "cniVersion is not set")
	}

	rawPlugins, isList := top["plugins"]
	_, hasType := top["type"]
	switch {
	case isList:
		cfg.IsList = true
		if hasType {
			cfg.Warnings = append(cfg.Warnings, `both "type" and "plugins" are set; "type" is ignored for configuration lists`)
		}
		if cfg.Name == "" {
			cfg.Warnings = append(cfg.Warnings, "configuration list has no name")
		}
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(rawPlugins, &list); err != nil {
			return nil, fmt.Errorf(`"plugins" must be a list of objects: %w`, err)
		}
		if len(list) == 0 {
			return nil, errors.New(`"plugins" list is empty`)
		}
		for i, p := range list {
			plugin, err := parsePlugin(p)
			if err != nil {
				return nil, fmt.Errorf("plugins[%d]: %w", i, err)
			}
			cfg.Plugins = append(cfg.Plugins, plugin)
		}
	case hasType:
		plugin, err := parsePlugin(top)
		if err != nil {
			return nil, err
		}
		cfg.Plugins = []CNIPlugin{plugin}
	default:
		return nil, errors.New(`configuration has neither "type" nor "plugins"`)
	}

	if cfg.Primary() == nil && !hasUnknown(cfg.Plugins) {
		cfg.Warnings = append(cfg.Warnings, "no plugin in the chain creates an interface; only meta plugins are configured")
	}
	if cfg.IsList && len(cfg.Plugins) > 1 && cfg.Plugins[0].Role == RoleMeta {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("first plugin %q is a meta plugin; meta plugins normally follow the interface plugin", cfg.Plugins[0].Type))
	}
	return cfg, nil
}

func hasUnknown(plugins []CNIPlugin) bool {
	for _, p := range plugins {
		if p.Role == RoleUnknown {
			return true
		}
	}
	return false
}

func parsePlugin(fields map[string]json.RawMessage) (CNIPlugin, error) {
	var p CNIPlugin
	if err := optionalString(fields, "type", &p.Type); err != nil {
		return p, err
	}
	if p.Type == "" {
		return p, errors.New(`plugin has no "type"`)
	}
	if info, ok := knownPlugins[p.Type]; ok {
		p.Description, p.Role = info.description, info.role
	} else {
		p.Description, p.Role = "unrecognized plugin type", RoleUnknown
	}

	for key, dst := range map[string]*string{
		"bridge": &p.Bridge, "master": &p.Master, "mode": &p.Mode,
		"device": &p.Device, "topology": &p.Topology,
	} {
		if err := optionalString(fields, key, dst); err != nil {
			return p, err
		}
	}

	var err error
	if p.VLAN, err = optionalInt(fields, "vlan"); err != nil {
		return p, err
	}
	if p.VLAN != nil && (*p.VLAN < 0 || *p.VLAN > 4094) {
		return p, fmt.Errorf(`"vlan" %d is outside the valid range 0-4094`, *p.VLAN)
	}
	if p.MTU, err = optionalInt(fields, "mtu"); err != nil {
		return p, err
	}
	// Linux bridge CNI uses "vlanTrunk"; OVS CNI uses "trunk".
	_, lbTrunk := fields["vlanTrunk"]
	_, ovsTrunk := fields["trunk"]
	p.Trunk = lbTrunk || ovsTrunk

	if rawIPAM, ok := fields["ipam"]; ok && !isJSONNull(rawIPAM) {
		var ipam map[string]json.RawMessage
		if err := json.Unmarshal(rawIPAM, &ipam); err != nil {
			return p, fmt.Errorf(`"ipam" must be an object: %w`, err)
		}
		if err := optionalString(ipam, "type", &p.IPAM); err != nil {
			return p, fmt.Errorf("ipam: %w", err)
		}
	}
	return p, nil
}

func optionalString(fields map[string]json.RawMessage, key string, dst *string) error {
	raw, ok := fields[key]
	if !ok || isJSONNull(raw) {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%q must be a string", key)
	}
	return nil
}

func optionalInt(fields map[string]json.RawMessage, key string) (*int, error) {
	raw, ok := fields[key]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%q must be an integer", key)
	}
	return &v, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// strictUnmarshal decodes a single JSON value and rejects trailing data.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON object")
	}
	return nil
}

// Summary renders the plugin chain on one line, e.g.
// "bridge(br=br1,vlan=100) -> tuning".
func (c *CNIConfig) Summary() string {
	parts := make([]string, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		var attrs []string
		if p.Bridge != "" {
			attrs = append(attrs, "br="+p.Bridge)
		}
		if p.Master != "" {
			attrs = append(attrs, "master="+p.Master)
		}
		if p.Device != "" {
			attrs = append(attrs, "device="+p.Device)
		}
		if p.Topology != "" {
			attrs = append(attrs, "topology="+p.Topology)
		}
		if p.VLAN != nil {
			attrs = append(attrs, fmt.Sprintf("vlan=%d", *p.VLAN))
		}
		if p.Trunk {
			attrs = append(attrs, "trunk")
		}
		s := p.Type
		if len(attrs) > 0 {
			s += "(" + strings.Join(attrs, ",") + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " -> ")
}
