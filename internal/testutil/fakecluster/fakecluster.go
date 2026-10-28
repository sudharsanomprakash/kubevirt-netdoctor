// Package fakecluster builds fake Kubernetes clients from YAML fixtures for
// tests. It is not used by production code.
package fakecluster

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// Option customizes the fake cluster.
type Option func(*config)

type config struct {
	withoutNADAPI bool
	forbidRules   []forbidRule
}

type forbidRule struct {
	verb, resource string
}

// WithoutNADAPI simulates a cluster where Multus (the NAD CRD) is absent.
func WithoutNADAPI() Option {
	return func(c *config) { c.withoutNADAPI = true }
}

// Forbid makes every request with the verb on the resource (plural name,
// e.g. "pods" or "network-attachment-definitions") fail with Forbidden, as
// the API server does when RBAC denies access.
func Forbid(verb, resource string) Option {
	return func(c *config) { c.forbidRules = append(c.forbidRules, forbidRule{verb, resource}) }
}

// New loads the YAML fixture files and returns clients backed by fake
// object trackers.
func New(t testing.TB, files []string, opts ...Option) *kube.Clients {
	t.Helper()
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.VirtualMachineGVR:              "VirtualMachineList",
		kube.VirtualMachineInstanceGVR:      "VirtualMachineInstanceList",
		kube.NetworkAttachmentDefinitionGVR: "NetworkAttachmentDefinitionList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	core := fake.NewClientset()

	for _, f := range files {
		for _, obj := range loadFile(t, f) {
			addObject(t, obj, dyn, core)
		}
	}

	disc := core.Discovery().(*fakediscovery.FakeDiscovery)
	disc.Resources = []*metav1.APIResourceList{{
		GroupVersion: "kubevirt.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "virtualmachines", Namespaced: true, Kind: "VirtualMachine"},
			{Name: "virtualmachineinstances", Namespaced: true, Kind: "VirtualMachineInstance"},
		},
	}}
	if !cfg.withoutNADAPI {
		disc.Resources = append(disc.Resources, &metav1.APIResourceList{
			GroupVersion: "k8s.cni.cncf.io/v1",
			APIResources: []metav1.APIResource{
				{Name: "network-attachment-definitions", Namespaced: true, Kind: "NetworkAttachmentDefinition"},
			},
		})
	}

	for _, r := range cfg.forbidRules {
		reactor := forbidReactor(r)
		dyn.PrependReactor(r.verb, r.resource, reactor)
		core.PrependReactor(r.verb, r.resource, reactor)
	}
	return &kube.Clients{Core: core, Dynamic: dyn}
}

func forbidReactor(r forbidRule) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		gr := action.GetResource().GroupResource()
		name := ""
		if g, ok := action.(k8stesting.GetAction); ok {
			name = g.GetName()
		}
		return true, nil, apierrors.NewForbidden(gr, name,
			errors.New(`User "system:serviceaccount:test:netdoctor" cannot `+r.verb+` resource "`+r.resource+`"`))
	}
}

func loadFile(t testing.TB, path string) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", path, err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("decoding fixture %s: %v", path, err)
		}
		if len(obj) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: obj})
	}
}

func addObject(t testing.TB, u *unstructured.Unstructured, dyn *dynamicfake.FakeDynamicClient, core *fake.Clientset) {
	t.Helper()
	var gvr schema.GroupVersionResource
	switch u.GetKind() {
	case "Pod":
		pod := &corev1.Pod{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, pod); err != nil {
			t.Fatalf("converting pod %s: %v", u.GetName(), err)
		}
		if err := core.Tracker().Add(pod); err != nil {
			t.Fatalf("adding pod %s: %v", pod.Name, err)
		}
		return
	case "VirtualMachine":
		gvr = kube.VirtualMachineGVR
	case "VirtualMachineInstance":
		gvr = kube.VirtualMachineInstanceGVR
	case "NetworkAttachmentDefinition":
		gvr = kube.NetworkAttachmentDefinitionGVR
	default:
		t.Fatalf("fixture kind %q is not supported", u.GetKind())
	}
	if err := dyn.Tracker().Create(gvr, u, u.GetNamespace()); err != nil {
		t.Fatalf("adding %s %s: %v", u.GetKind(), u.GetName(), err)
	}
}
