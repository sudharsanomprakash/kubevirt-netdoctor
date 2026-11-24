// Package capability reports what netdoctor can observe in a cluster with
// the caller's credentials: which APIs are served and which reads RBAC
// allows. It only performs discovery and SelfSubjectAccessReviews.
package capability

import (
	"context"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// Access results.
const (
	Allowed = "Allowed"
	Denied  = "Denied"
)

// Status values.
const (
	Available   = "Available"
	Unavailable = "Unavailable"
	Degraded    = "Degraded"
	Unknown     = "Unknown"
)

// API is one API resource netdoctor uses or recognizes.
type API struct {
	Resource string `json:"resource"`
	Required bool   `json:"required"`
	Served   string `json:"served"`
	Purpose  string `json:"purpose"`
	Error    string `json:"error,omitempty"`
}

// Access is one RBAC check.
type Access struct {
	Verb     string `json:"verb"`
	Resource string `json:"resource"`
	Allowed  string `json:"allowed"`
	Reason   string `json:"reason,omitempty"`
}

// Feature is a netdoctor feature and whether it works with this access.
type Feature struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report is the result of a cluster capability check.
type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Namespace     string    `json:"namespace"`
	APIs          []API     `json:"apis"`
	Access        []Access  `json:"access"`
	Features      []Feature `json:"features"`
}

type apiDef struct {
	gvr      schema.GroupVersionResource
	required bool
	purpose  string
}

var apis = []apiDef{
	{kube.VirtualMachineGVR, true, "VM lookup"},
	{kube.VirtualMachineInstanceGVR, true, "running instance, interfaces and node"},
	{kube.NetworkAttachmentDefinitionGVR, false, "Multus secondary network configuration"},
	{kube.NetworkPolicyGVR, false, "NetworkPolicies that may select the launcher pod"},
	{schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "userdefinednetworks"}, false, "OVN-Kubernetes user-defined networks (presence indicates OVN-Kubernetes)"},
	{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}, false, "Kube-OVN subnets (presence indicates Kube-OVN)"},
	{schema.GroupVersionResource{Group: "nmstate.io", Version: "v1beta1", Resource: "nodenetworkstates"}, false, "kubernetes-nmstate node state (presence indicates nmstate)"},
}

type accessDef struct {
	verb string
	gvr  schema.GroupVersionResource
}

var reads = []accessDef{
	{"get", kube.VirtualMachineGVR},
	{"get", kube.VirtualMachineInstanceGVR},
	{"list", kube.PodGVR},
	{"get", kube.NetworkAttachmentDefinitionGVR},
	{"list", kube.NetworkPolicyGVR},
}

// Check runs discovery and access reviews for namespace.
func Check(ctx context.Context, c *kube.Clients, namespace string) *Report {
	r := &Report{SchemaVersion: "netdoctor/v1alpha1", Kind: "ClusterCapabilities", Namespace: namespace}
	served := map[schema.GroupVersionResource]string{}
	for _, a := range apis {
		ok, err := kube.APIServed(ctx, c, a.gvr)
		e := API{Resource: kube.ResourceString(a.gvr), Required: a.required, Purpose: a.purpose, Served: Unavailable}
		switch {
		case err != nil:
			e.Served, e.Error = Unknown, err.Error()
		case ok:
			e.Served = Available
		}
		served[a.gvr] = e.Served
		r.APIs = append(r.APIs, e)
	}
	allowed := map[string]string{}
	for _, a := range reads {
		e := Access{Verb: a.verb, Resource: kube.ResourceString(a.gvr), Allowed: Unknown}
		res, err := c.Core.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace, Verb: a.verb, Group: a.gvr.Group, Resource: a.gvr.Resource,
			}},
		}, metav1.CreateOptions{})
		switch {
		case err != nil:
			e.Reason = err.Error()
		case res.Status.Allowed:
			e.Allowed = Allowed
		default:
			e.Allowed = Denied
			e.Reason = res.Status.Reason
		}
		allowed[a.verb+" "+e.Resource] = e.Allowed
		r.Access = append(r.Access, e)
	}

	can := func(verb string, gvr schema.GroupVersionResource) string {
		if served[gvr] == Unavailable {
			return Unavailable
		}
		switch allowed[verb+" "+kube.ResourceString(gvr)] {
		case Allowed:
			return Available
		case Denied:
			return Unavailable
		}
		return Unknown
	}
	vmi := can("get", kube.VirtualMachineInstanceGVR)
	pods := can("list", kube.PodGVR)
	nad := can("get", kube.NetworkAttachmentDefinitionGVR)
	core := Available
	detail := "VM, VMI and virt-launcher pod reads are allowed"
	switch {
	case vmi == Unavailable:
		core, detail = Unavailable, "KubeVirt VMIs cannot be read (API not served or access denied)"
	case pods == Unavailable:
		core, detail = Degraded, "virt-launcher pods cannot be listed: pod attachments and node correlation are unavailable"
	case vmi == Unknown || pods == Unknown:
		core, detail = Unknown, "access could not be determined"
	}
	r.Features = append(r.Features,
		Feature{Name: "inspect / topology / diagnose", Status: core, Detail: detail},
		Feature{Name: "trace plan", Status: core, Detail: "needs the VMI and its virt-launcher pod"},
	)
	switch nad {
	case Available:
		r.Features = append(r.Features, Feature{Name: "Multus NAD analysis", Status: Available})
	case Unavailable:
		r.Features = append(r.Features, Feature{Name: "Multus NAD analysis", Status: Unavailable,
			Detail: "NetworkAttachmentDefinitions are not served or not readable; secondary networks are reported as not observed"})
	default:
		r.Features = append(r.Features, Feature{Name: "Multus NAD analysis", Status: Unknown})
	}
	r.Features = append(r.Features, Feature{Name: "host collect / host trace", Status: Unknown,
		Detail: "node-side; run 'kubectl-netdoctor host capabilities' on the node"})
	return r
}
