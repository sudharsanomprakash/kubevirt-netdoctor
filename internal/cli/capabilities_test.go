package cli

import (
	"encoding/json"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/capability"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/ebpf"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
)

// allowExcept answers SelfSubjectAccessReviews: everything is allowed
// except the listed "verb resource" pairs.
func allowExcept(denied ...string) k8stesting.ReactionFunc {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		attr := r.Spec.ResourceAttributes
		r.Status.Allowed = true
		for _, d := range denied {
			if d == attr.Verb+" "+attr.Resource {
				r.Status.Allowed = false
				r.Status.Reason = "RBAC: denied by test"
			}
		}
		return true, r, nil
	}
}

func TestCapabilitiesText(t *testing.T) {
	clients := fakecluster.New(t, nil)
	clients.Core.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", allowExcept())
	out, err := run(t, clients, "capabilities", "-n", "demo")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "capabilities.txt", out)
}

func TestCapabilitiesDegraded(t *testing.T) {
	clients := fakecluster.New(t, nil, fakecluster.WithoutNADAPI())
	clients.Core.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", allowExcept("list pods"))
	out, err := run(t, clients, "capabilities", "-n", "demo", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r capability.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, f := range r.Features {
		status[f.Name] = f.Status
	}
	if status["inspect / topology / diagnose"] != capability.Degraded || status["Multus NAD analysis"] != capability.Unavailable {
		t.Errorf("features %+v", r.Features)
	}
}

func TestHostCapabilities(t *testing.T) {
	env := HostEnv{Hostname: func() (string, error) { return "n", nil }, Probe: func(load bool) ebpf.NodeCapabilities {
		st := ebpf.CapAvailable
		if !load {
			st = ebpf.CapNotChecked
		}
		return ebpf.NodeCapabilities{Kernel: "6.8.0", Arch: "amd64", Tracing: st, Checks: []ebpf.Capability{
			{Name: "kernel BTF", Status: ebpf.CapAvailable, Detail: "/sys/kernel/btf/vmlinux"},
			{Name: "drop locations", Status: ebpf.CapDegraded, Detail: "hidden", Impact: "raw addresses"},
		}}
	}}
	out, _, err := runHostEnv(t, env, "host", "capabilities")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "eBPF tracing: Available") || !strings.Contains(out, "Degraded     drop locations: hidden (raw addresses)") {
		t.Errorf("text:\n%s", out)
	}
	out, _, err = runHostEnv(t, env, "host", "capabilities", "--load=false", "-o", "json")
	if err != nil || !strings.Contains(out, `"tracing": "NotChecked"`) {
		t.Errorf("json: %v\n%s", err, out)
	}
	if _, _, err := runHostEnv(t, env, "host", "capabilities", "-o", "yaml"); err == nil {
		t.Error("yaml must be rejected")
	}
}
