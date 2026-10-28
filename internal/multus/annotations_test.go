package multus

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseNetworkRef(t *testing.T) {
	tests := []struct {
		in      string
		want    NetworkRef
		wantErr bool
	}{
		{"br1", NetworkRef{"demo", "br1"}, false},
		{"infra/br1", NetworkRef{"infra", "br1"}, false},
		{" infra/br1 ", NetworkRef{"infra", "br1"}, false},
		{"", NetworkRef{}, true},
		{"/br1", NetworkRef{}, true},
		{"infra/", NetworkRef{}, true},
		{"a/b/c", NetworkRef{}, true},
	}
	for _, tc := range tests {
		got, err := ParseNetworkRef(tc.in, "demo")
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseNetworkRef(%q) = %v, %v; want %v, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParseNetworkSelections(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []NetworkSelection
		wantErr bool
	}{
		{"empty", "", nil, false},
		{
			name: "kubevirt json form",
			in:   `[{"interface":"pod5c6f2b9e0d1","mac":"02:00:00:00:64:01","name":"br-vlan100","namespace":"demo"}]`,
			want: []NetworkSelection{{Name: "br-vlan100", Namespace: "demo", Interface: "pod5c6f2b9e0d1", MAC: "02:00:00:00:64:01"}},
		},
		{
			name: "short form",
			in:   "br1, infra/ovs@net2,plain",
			want: []NetworkSelection{
				{Name: "br1"},
				{Name: "ovs", Namespace: "infra", Interface: "net2"},
				{Name: "plain"},
			},
		},
		{"malformed json", `[{"name":`, nil, true},
		{"json element without name", `[{"namespace":"demo"}]`, nil, true},
		{"short form bad entry", "a/b/c", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNetworkSelections(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNetworkSelectionRefDefaultsToPodNamespace(t *testing.T) {
	if got := (NetworkSelection{Name: "br1"}).Ref("demo"); got != (NetworkRef{"demo", "br1"}) {
		t.Errorf("Ref = %v", got)
	}
}

func TestParseNetworkStatus(t *testing.T) {
	in := `[{"name":"k8s-pod-network","interface":"eth0","ips":["10.244.1.15"],"mac":"0a:58:0a:f4:01:0f","default":true,"dns":{}},
	        {"name":"demo/br-vlan100","interface":"pod5c6f2b9e0d1","mac":"02:00:00:00:64:01","dns":{}}]`
	got, err := ParseNetworkStatus(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []NetworkStatus{
		{Name: "k8s-pod-network", Interface: "eth0", IPs: []string{"10.244.1.15"}, MAC: "0a:58:0a:f4:01:0f", Default: true},
		{Name: "demo/br-vlan100", Interface: "pod5c6f2b9e0d1", MAC: "02:00:00:00:64:01"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := ParseNetworkStatus("{not json"); err == nil {
		t.Error("expected error for malformed annotation")
	}
}

func TestNetworkStatusMatchesRef(t *testing.T) {
	ref := NetworkRef{"demo", "br1"}
	tests := []struct {
		name string
		want bool
	}{
		{"demo/br1", true},
		{"br1", true}, // bare name, same namespace as the pod
		{"infra/br1", false},
		{"br2", false},
	}
	for _, tc := range tests {
		if got := (NetworkStatus{Name: tc.name}).MatchesRef(ref, "demo"); got != tc.want {
			t.Errorf("MatchesRef(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (NetworkStatus{Name: "br1"}).MatchesRef(NetworkRef{"infra", "br1"}, "demo") {
		t.Error("bare name must not match a NAD in another namespace")
	}
}

func TestReadPodNetworking(t *testing.T) {
	if pn := ReadPodNetworking(nil); pn.StatusPresent || pn.Selections != nil {
		t.Errorf("nil pod: %+v", pn)
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		NetworksAnnotation:       "br1",
		DefaultNetworkAnnotation: "demo/ovs",
	}}}
	pn := ReadPodNetworking(pod)
	if pn.StatusPresent {
		t.Error("status must be absent when the annotation is missing")
	}
	if len(pn.Selections) != 1 || pn.DefaultNetwork != "demo/ovs" {
		t.Errorf("unexpected %+v", pn)
	}

	pod.Annotations[NetworkStatusAnnotation] = "garbage"
	pn = ReadPodNetworking(pod)
	if !pn.StatusPresent || pn.StatusErr == nil {
		t.Errorf("malformed status must be present with an error: %+v", pn)
	}
}
