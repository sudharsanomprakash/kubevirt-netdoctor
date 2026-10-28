// Package kube builds the Kubernetes API clients used by netdoctor and
// classifies API errors into the access states reported to users.
package kube

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Group/version/resources read by netdoctor. All access is read-only.
var (
	VirtualMachineGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines",
	}
	VirtualMachineInstanceGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances",
	}
	NetworkAttachmentDefinitionGVR = schema.GroupVersionResource{
		Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions",
	}
	PodGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// Clients bundles the API clients needed for an inspection.
type Clients struct {
	Core    kubernetes.Interface
	Dynamic dynamic.Interface
}

// defaultRequestTimeout bounds every individual API request when the user did
// not set --request-timeout.
const defaultRequestTimeout = 15 * time.Second

// NewClients builds clients from kubectl-style configuration flags
// (--kubeconfig, --context, ...).
func NewClients(getter genericclioptions.RESTClientGetter) (*Clients, error) {
	cfg, err := getter.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultRequestTimeout
	}
	cfg.UserAgent = "kubectl-netdoctor"

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating core client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return &Clients{Core: core, Dynamic: dyn}, nil
}

// LookupState records the outcome of reading a Kubernetes object, so that
// "not found" is never confused with "not allowed to look".
type LookupState string

const (
	StateFound     LookupState = "Found"
	StateNotFound  LookupState = "NotFound"
	StateForbidden LookupState = "Forbidden"
	// StateUnavailable means the API could not answer (API group not served,
	// timeout, server error).
	StateUnavailable LookupState = "Unavailable"
	// StateNotChecked means the lookup was intentionally skipped.
	StateNotChecked LookupState = "NotChecked"
)

// Classify maps an API error to a LookupState. A nil error is StateFound.
func Classify(err error) LookupState {
	switch {
	case err == nil:
		return StateFound
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return StateForbidden
	case apierrors.IsNotFound(err):
		return StateNotFound
	default:
		return StateUnavailable
	}
}

// AccessIssue describes an API request that could not be answered.
type AccessIssue struct {
	Verb      string      `json:"verb"`
	Resource  string      `json:"resource"`
	Namespace string      `json:"namespace,omitempty"`
	Name      string      `json:"name,omitempty"`
	State     LookupState `json:"state"`
	Message   string      `json:"message"`
}

// NewAccessIssue builds an AccessIssue from a failed request.
func NewAccessIssue(verb string, gvr schema.GroupVersionResource, namespace, name string, err error) AccessIssue {
	return AccessIssue{
		Verb:      verb,
		Resource:  ResourceString(gvr),
		Namespace: namespace,
		Name:      name,
		State:     Classify(err),
		Message:   err.Error(),
	}
}

// ResourceString renders a GVR the way RBAC rules name it (resource.group).
func ResourceString(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

// APIServed reports whether the cluster serves the group/version of gvr and
// the resource within it. It returns an error only when discovery itself
// failed for reasons other than the group/version being absent.
func APIServed(ctx context.Context, c *Clients, gvr schema.GroupVersionResource) (bool, error) {
	disc := discovery.ToDiscoveryInterfaceWithContext(c.Core.Discovery())
	list, err := disc.ServerResourcesForGroupVersionWithContext(ctx, gvr.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, r := range list.APIResources {
		if r.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}
