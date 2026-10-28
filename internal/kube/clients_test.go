package kube

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClassify(t *testing.T) {
	gr := schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachines"}
	tests := []struct {
		err  error
		want LookupState
	}{
		{nil, StateFound},
		{apierrors.NewNotFound(gr, "x"), StateNotFound},
		{apierrors.NewForbidden(gr, "x", errors.New("denied")), StateForbidden},
		{apierrors.NewUnauthorized("expired"), StateForbidden},
		{apierrors.NewTimeoutError("slow", 1), StateUnavailable},
		{apierrors.NewInternalError(errors.New("boom")), StateUnavailable},
		{context.DeadlineExceeded, StateUnavailable},
	}
	for _, tc := range tests {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestNewAccessIssue(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	a := NewAccessIssue("list", PodGVR, "demo", "", err)
	if a.Resource != "pods" || a.State != StateForbidden || a.Verb != "list" || a.Namespace != "demo" {
		t.Errorf("unexpected %+v", a)
	}
	if got := ResourceString(NetworkAttachmentDefinitionGVR); got != "network-attachment-definitions.k8s.cni.cncf.io" {
		t.Errorf("ResourceString = %s", got)
	}
}

func TestAPIServed(t *testing.T) {
	core := fake.NewClientset()
	core.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{
		GroupVersion: "kubevirt.io/v1",
		APIResources: []metav1.APIResource{{Name: "virtualmachines"}},
	}}
	c := &Clients{Core: core}
	ctx := context.Background()

	if ok, err := APIServed(ctx, c, VirtualMachineGVR); !ok || err != nil {
		t.Errorf("virtualmachines: %v %v", ok, err)
	}
	if ok, err := APIServed(ctx, c, VirtualMachineInstanceGVR); ok || err != nil {
		t.Errorf("resource missing from served group: %v %v", ok, err)
	}
	if ok, err := APIServed(ctx, c, NetworkAttachmentDefinitionGVR); ok || err != nil {
		t.Errorf("group not served: %v %v", ok, err)
	}
}
