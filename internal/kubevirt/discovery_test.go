package kubevirt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

const vmiUID = types.UID("vmi-uid")

func launcherPod(name string, uid types.UID, node string, phase corev1.PodPhase, created time.Time, owned bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "demo", UID: uid,
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{LabelCreatedBy: string(vmiUID), LabelApp: LauncherAppValue},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase},
	}
	if owned {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "VirtualMachineInstance", Name: "vm", UID: vmiUID}}
	}
	return p
}

func testVMI(node string, active map[types.UID]string) *VirtualMachineInstance {
	return &VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "vm", Namespace: "demo", UID: vmiUID},
		Status:     VMIStatus{Phase: PhaseRunning, NodeName: node, ActivePods: active},
	}
}

func TestListLauncherPodsFiltersByOwnerAndSorts(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	other := launcherPod("other-vmi", "u4", "w1", corev1.PodRunning, t0, true)
	other.Labels[LabelCreatedBy] = "another-uid"
	client := fake.NewClientset(
		launcherPod("newer", "u1", "w2", corev1.PodRunning, t0.Add(time.Hour), true),
		launcherPod("older", "u2", "w1", corev1.PodSucceeded, t0, true),
		launcherPod("copied-label", "u3", "w1", corev1.PodRunning, t0, false),
		other,
	)
	pods, err := ListLauncherPods(context.Background(), client, testVMI("w2", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 2 || pods[0].Name != "older" || pods[1].Name != "newer" {
		names := []string{}
		for _, p := range pods {
			names = append(names, p.Name)
		}
		t.Fatalf("pods = %v, want [older newer]", names)
	}
}

func TestSelectLauncherPod(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	running := func(name string, uid types.UID, node string) corev1.Pod {
		return *launcherPod(name, uid, node, corev1.PodRunning, t0, true)
	}
	done := *launcherPod("done", "u0", "w3", corev1.PodSucceeded, t0, true)

	tests := []struct {
		name           string
		vmi            *VirtualMachineInstance
		pods           []corev1.Pod
		wantSelected   string
		wantCandidates int
		wantTerminated int
	}{
		{"none", testVMI("w1", nil), nil, "", 0, 0},
		{"only terminated", testVMI("w1", nil), []corev1.Pod{done}, "", 0, 1},
		{"single", testVMI("w1", nil), []corev1.Pod{done, running("a", "u1", "w1")}, "a", 1, 1},
		{"migration picks vmi node", testVMI("w1", map[types.UID]string{"u1": "w1", "u2": "w2"}),
			[]corev1.Pod{running("src", "u1", "w1"), running("tgt", "u2", "w2")}, "src", 2, 0},
		{"active pods narrow candidates", testVMI("", map[types.UID]string{"u2": "w2"}),
			[]corev1.Pod{running("stale", "u1", "w1"), running("live", "u2", "w2")}, "live", 2, 0},
		{"ambiguous on same node", testVMI("w1", nil),
			[]corev1.Pod{running("a", "u1", "w1"), running("b", "u2", "w1")}, "", 2, 0},
		{"ambiguous without node", testVMI("", nil),
			[]corev1.Pod{running("a", "u1", "w1"), running("b", "u2", "w2")}, "", 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel := SelectLauncherPod(tc.vmi, tc.pods)
			got := ""
			if sel.Selected != nil {
				got = sel.Selected.Name
			}
			if got != tc.wantSelected {
				t.Errorf("selected = %q, want %q (reason: %s)", got, tc.wantSelected, sel.Reason)
			}
			if len(sel.Candidates) != tc.wantCandidates || len(sel.Terminated) != tc.wantTerminated {
				t.Errorf("candidates=%d terminated=%d, want %d/%d", len(sel.Candidates), len(sel.Terminated), tc.wantCandidates, tc.wantTerminated)
			}
			if sel.Reason == "" {
				t.Error("selection reason must always be set")
			}
		})
	}
}

func TestEffectiveRunStrategy(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		spec VirtualMachineSpec
		want string
	}{
		{VirtualMachineSpec{RunStrategy: RunStrategyManual, Running: &yes}, RunStrategyManual},
		{VirtualMachineSpec{Running: &yes}, RunStrategyAlways},
		{VirtualMachineSpec{Running: &no}, RunStrategyHalted},
		{VirtualMachineSpec{}, ""},
	}
	for _, tc := range tests {
		vm := &VirtualMachine{Spec: tc.spec}
		if got := vm.EffectiveRunStrategy(); got != tc.want {
			t.Errorf("EffectiveRunStrategy(%+v) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}

func TestBindingMethodDecoding(t *testing.T) {
	tests := map[string]string{
		`{"name":"a","bridge":{}}`:                   "bridge",
		`{"name":"a","masquerade":{}}`:               "masquerade",
		`{"name":"a","sriov":{}}`:                    "sriov",
		`{"name":"a","binding":{"name":"passt"}}`:    "plugin:passt",
		`{"name":"a","binding":{"name":"l2bridge"}}`: "plugin:l2bridge",
		`{"name":"a"}`:                               "unknown",
	}
	for raw, want := range tests {
		var i Interface
		if err := json.Unmarshal([]byte(raw), &i); err != nil {
			t.Fatal(err)
		}
		if got := i.BindingMethod(); got != want {
			t.Errorf("%s: BindingMethod() = %q, want %q", raw, got, want)
		}
	}
}

func TestInterfaceStatusAllIPs(t *testing.T) {
	s := InterfaceStatus{IP: "192.0.2.1", IPs: []string{"192.0.2.1", "2001:db8::1"}}
	if got := s.AllIPs(); len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "2001:db8::1" {
		t.Errorf("AllIPs() = %v", got)
	}
	if got := (InterfaceStatus{}).AllIPs(); got != nil {
		t.Errorf("empty AllIPs() = %v", got)
	}
}

func TestOwnerVMNameRequiresController(t *testing.T) {
	ctrl, notCtrl := true, false
	vmi := &VirtualMachineInstance{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
		{Kind: "VirtualMachine", Name: "not-controller", Controller: &notCtrl},
		{Kind: "VirtualMachine", Name: "vm", Controller: &ctrl},
	}}}
	if got := vmi.OwnerVMName(); got != "vm" {
		t.Errorf("OwnerVMName() = %q", got)
	}
}
