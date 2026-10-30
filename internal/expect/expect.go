// Package expect loads user-declared network expectations. netdoctor never
// invents expected VLANs, MTUs or LACP partners: such checks run only against
// the NAD configuration or values the user states in an expectations file.
package expect

import (
	"fmt"
	"net"
	"os"

	"sigs.k8s.io/yaml"
)

// Spec is the expectations file.
//
//	networks:
//	  vlan100:            # VMI spec.networks[].name
//	    vlan: 100
//	    mtu: 9000
//	bonds:
//	  bond0:              # host bond (Linux bond or OVS bond port) name
//	    partnerSystemID: "02:aa:00:00:00:01"
//	    members: [eno1, eno2]
type Spec struct {
	Networks map[string]Network `json:"networks,omitempty"`
	Bonds    map[string]Bond    `json:"bonds,omitempty"`
}

// Network holds expectations for one VMI network.
type Network struct {
	VLAN *int `json:"vlan,omitempty"`
	MTU  *int `json:"mtu,omitempty"`
}

// Bond holds expectations for one host bond.
type Bond struct {
	PartnerSystemID string   `json:"partnerSystemID,omitempty"`
	Members         []string `json:"members,omitempty"`
}

// Load reads and validates an expectations file. Unknown fields are
// rejected so typos do not silently disable checks.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an explicit CLI argument
	if err != nil {
		return nil, fmt.Errorf("reading expectations: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates expectations.
func Parse(data []byte) (*Spec, error) {
	var s Spec
	if err := yaml.UnmarshalStrict(data, &s); err != nil {
		return nil, fmt.Errorf("parsing expectations: %w", err)
	}
	for name, n := range s.Networks {
		if n.VLAN != nil && (*n.VLAN < 0 || *n.VLAN > 4094) {
			return nil, fmt.Errorf("expectations: networks.%s.vlan %d is outside 0-4094", name, *n.VLAN)
		}
		if n.MTU != nil && (*n.MTU < 68 || *n.MTU > 65535) {
			return nil, fmt.Errorf("expectations: networks.%s.mtu %d is outside 68-65535", name, *n.MTU)
		}
	}
	for name, b := range s.Bonds {
		if b.PartnerSystemID != "" {
			if _, err := net.ParseMAC(b.PartnerSystemID); err != nil {
				return nil, fmt.Errorf("expectations: bonds.%s.partnerSystemID %q is not a MAC address", name, b.PartnerSystemID)
			}
		}
	}
	return &s, nil
}

// Network returns the expectations for a VMI network, if any.
func (s *Spec) Network(name string) (Network, bool) {
	if s == nil {
		return Network{}, false
	}
	n, ok := s.Networks[name]
	return n, ok
}

// Bond returns the expectations for a bond, if any.
func (s *Spec) Bond(name string) (Bond, bool) {
	if s == nil {
		return Bond{}, false
	}
	b, ok := s.Bonds[name]
	return b, ok
}
