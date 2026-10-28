// Package multus reads Multus CNI pod annotations and
// NetworkAttachmentDefinition objects, and parses NAD CNI configuration.
package multus

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Annotation keys defined by the Kubernetes Network Plumbing Working Group
// multi-network specification and Multus.
const (
	NetworksAnnotation       = "k8s.v1.cni.cncf.io/networks"
	NetworkStatusAnnotation  = "k8s.v1.cni.cncf.io/network-status"
	DefaultNetworkAnnotation = "v1.multus-cni.io/default-network"
	ResourceNameAnnotation   = "k8s.v1.cni.cncf.io/resourceName"
)

// NetworkRef identifies a NetworkAttachmentDefinition.
type NetworkRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (r NetworkRef) String() string { return r.Namespace + "/" + r.Name }

// ParseNetworkRef parses "name" or "namespace/name". A bare name resolves to
// defaultNamespace, matching how KubeVirt and Multus resolve references.
func ParseNetworkRef(s, defaultNamespace string) (NetworkRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return NetworkRef{}, fmt.Errorf("empty network reference")
	}
	parts := strings.Split(s, "/")
	switch {
	case len(parts) == 1:
		return NetworkRef{Namespace: defaultNamespace, Name: parts[0]}, nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return NetworkRef{Namespace: parts[0], Name: parts[1]}, nil
	default:
		return NetworkRef{}, fmt.Errorf("invalid network reference %q: expected <name> or <namespace>/<name>", s)
	}
}

// NetworkSelection is one entry of the k8s.v1.cni.cncf.io/networks pod
// annotation: a request to attach the pod to a network.
type NetworkSelection struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Interface string `json:"interface,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

// Ref returns the NAD referenced by the selection.
func (s NetworkSelection) Ref(podNamespace string) NetworkRef {
	ns := s.Namespace
	if ns == "" {
		ns = podNamespace
	}
	return NetworkRef{Namespace: ns, Name: s.Name}
}

// ParseNetworkSelections parses the networks annotation, which may be a JSON
// list or the comma-separated "[namespace/]name[@interface]" short form.
func ParseNetworkSelections(value string) ([]NetworkSelection, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "[") {
		var out []NetworkSelection
		if err := json.Unmarshal([]byte(value), &out); err != nil {
			return nil, fmt.Errorf("parsing %s annotation: %w", NetworksAnnotation, err)
		}
		for i, s := range out {
			if s.Name == "" {
				return nil, fmt.Errorf("parsing %s annotation: element %d has no name", NetworksAnnotation, i)
			}
		}
		return out, nil
	}

	var out []NetworkSelection
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		var sel NetworkSelection
		if at := strings.LastIndex(item, "@"); at >= 0 {
			sel.Interface = item[at+1:]
			item = item[:at]
		}
		if slash := strings.Index(item, "/"); slash >= 0 {
			sel.Namespace, sel.Name = item[:slash], item[slash+1:]
		} else {
			sel.Name = item
		}
		if sel.Name == "" || strings.Contains(sel.Name, "/") {
			return nil, fmt.Errorf("parsing %s annotation: invalid entry %q", NetworksAnnotation, item)
		}
		out = append(out, sel)
	}
	return out, nil
}

// NetworkStatus is one entry of the k8s.v1.cni.cncf.io/network-status pod
// annotation, written by Multus after the attachment was set up.
type NetworkStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	MAC       string   `json:"mac,omitempty"`
	Default   bool     `json:"default,omitempty"`
}

// ParseNetworkStatus parses the network-status annotation.
func ParseNetworkStatus(value string) ([]NetworkStatus, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var out []NetworkStatus
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil, fmt.Errorf("parsing %s annotation: %w", NetworkStatusAnnotation, err)
	}
	return out, nil
}

// MatchesRef reports whether a network-status entry refers to the NAD.
// Multus records secondary networks as "namespace/name"; some versions record
// a bare name for NADs in the pod's own namespace.
func (s NetworkStatus) MatchesRef(ref NetworkRef, podNamespace string) bool {
	if s.Name == ref.String() {
		return true
	}
	return !strings.Contains(s.Name, "/") && ref.Namespace == podNamespace && s.Name == ref.Name
}
