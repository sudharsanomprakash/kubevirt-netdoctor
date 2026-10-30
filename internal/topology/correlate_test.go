package topology

import (
	"encoding/json"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
)

var marker = json.RawMessage("{}")

func bridgeIface(name, mac string) kubevirt.Interface {
	return kubevirt.Interface{Name: name, Bridge: &marker, MacAddress: mac}
}

func multusNet(name, nad string) kubevirt.Network {
	return kubevirt.Network{Name: name, Multus: &kubevirt.MultusNetwork{NetworkName: nad}}
}

func spec(ifaces []kubevirt.Interface, nets ...kubevirt.Network) kubevirt.VMISpec {
	return kubevirt.VMISpec{Domain: kubevirt.DomainSpec{Devices: kubevirt.Devices{Interfaces: ifaces}}, Networks: nets}
}

func found(name, config string) NADResult {
	return NADResult{Lookup: kube.StateFound, NAD: &multus.NetworkAttachmentDefinition{Namespace: "demo", Name: name, Config: config}}
}

func byName(t *testing.T, paths []NetworkPath, name string) NetworkPath {
	t.Helper()
	for _, p := range paths {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("network %q not found", name)
	return NetworkPath{}
}

func TestCorrelatePodAndMultusNetworks(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{{Name: "default", Masquerade: &marker}, bridgeIface("vlan100", "02:00:00:00:64:01")},
			kubevirt.Network{Name: "default", Pod: &kubevirt.PodNetwork{}},
			multusNet("vlan100", "br-vlan100"),
		),
		Status: &kubevirt.VMIStatus{Interfaces: []kubevirt.InterfaceStatus{
			{Name: "default", InterfaceName: "enp1s0", IP: "10.244.1.15"},
			{Name: "vlan100", InterfaceName: "enp2s0", IPs: []string{"192.0.2.10"}, MAC: "02:00:00:00:64:01"},
		}},
		Pod: multus.PodNetworking{
			Selections:    []multus.NetworkSelection{{Name: "br-vlan100", Namespace: "demo", Interface: "pod1"}},
			StatusPresent: true,
			Statuses: []multus.NetworkStatus{
				{Name: "k8s-pod-network", Interface: "eth0", Default: true, IPs: []string{"10.244.1.15"}},
				{Name: "demo/br-vlan100", Interface: "pod1", MAC: "02:00:00:00:64:01"},
			},
		},
		NADs: map[multus.NetworkRef]NADResult{
			{Namespace: "demo", Name: "br-vlan100"}: found("br-vlan100", `{"cniVersion":"0.3.1","type":"bridge","bridge":"br1","vlan":100}`),
		},
	}
	res := Correlate(in)

	def := byName(t, res.Networks, "default")
	if def.Source != SourcePod || def.Interface.Binding != "masquerade" {
		t.Errorf("default: %+v", def)
	}
	if def.PodAttachment == nil || def.PodAttachment.Interface != "eth0" || def.PodAttachment.NetworkName != "k8s-pod-network" {
		t.Errorf("default pod attachment: %+v", def.PodAttachment)
	}
	// The pod interface (eth0) and the guest interface (enp1s0) are distinct.
	if def.Guest == nil || def.Guest.InterfaceName != "enp1s0" {
		t.Errorf("default guest: %+v", def.Guest)
	}

	v := byName(t, res.Networks, "vlan100")
	if v.Source != SourceMultus || v.NAD == nil || v.NAD.Ref.String() != "demo/br-vlan100" || v.NAD.Lookup != kube.StateFound {
		t.Fatalf("vlan100 nad: %+v", v.NAD)
	}
	if v.NAD.Config == nil || v.NAD.Config.Summary() != "bridge(br=br1,vlan=100)" {
		t.Errorf("vlan100 config: %+v", v.NAD.Config)
	}
	if v.PodRequest == nil || v.PodRequest.Interface != "pod1" {
		t.Errorf("vlan100 request: %+v", v.PodRequest)
	}
	if v.PodAttachment == nil || v.PodAttachment.Interface != "pod1" {
		t.Errorf("vlan100 attachment: %+v", v.PodAttachment)
	}
	if v.Guest == nil || len(v.Guest.IPs) != 1 || v.Guest.IPs[0] != "192.0.2.10" {
		t.Errorf("vlan100 guest: %+v", v.Guest)
	}
}

// Two VMI networks attached to the same NAD must map to distinct pod
// interfaces rather than both claiming the first match.
func TestCorrelateSameNADTwice(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{bridgeIface("a", ""), bridgeIface("b", "")},
			multusNet("a", "br1"), multusNet("b", "demo/br1"),
		),
		Status: &kubevirt.VMIStatus{},
		Pod: multus.PodNetworking{
			Selections: []multus.NetworkSelection{
				{Name: "br1", Namespace: "demo", Interface: "podA"},
				{Name: "br1", Namespace: "demo", Interface: "podB"},
			},
			StatusPresent: true,
			Statuses: []multus.NetworkStatus{
				{Name: "demo/br1", Interface: "podB"},
				{Name: "demo/br1", Interface: "podA"},
			},
		},
	}
	res := Correlate(in)
	a, b := byName(t, res.Networks, "a"), byName(t, res.Networks, "b")
	if a.PodAttachment == nil || a.PodAttachment.Interface != "podA" {
		t.Errorf("a attachment: %+v", a.PodAttachment)
	}
	if b.PodAttachment == nil || b.PodAttachment.Interface != "podB" {
		t.Errorf("b attachment: %+v", b.PodAttachment)
	}
	if refs := MultusRefs(in.Spec, "demo"); len(refs) != 1 {
		t.Errorf("MultusRefs should de-duplicate, got %v", refs)
	}
}

// VMI status podInterfaceName takes precedence over request order.
func TestCorrelatePrefersPodInterfaceNameFromStatus(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{bridgeIface("a", ""), bridgeIface("b", "")},
			multusNet("a", "br1"), multusNet("b", "br1"),
		),
		Status: &kubevirt.VMIStatus{Interfaces: []kubevirt.InterfaceStatus{
			{Name: "a", PodInterfaceName: "net2"},
			{Name: "b", PodInterfaceName: "net1"},
		}},
		Pod: multus.PodNetworking{
			Selections: []multus.NetworkSelection{
				{Name: "br1", Interface: "net1"},
				{Name: "br1", Interface: "net2"},
			},
			StatusPresent: true,
			Statuses: []multus.NetworkStatus{
				{Name: "demo/br1", Interface: "net1"},
				{Name: "demo/br1", Interface: "net2"},
			},
		},
	}
	res := Correlate(in)
	if a := byName(t, res.Networks, "a"); a.PodAttachment == nil || a.PodAttachment.Interface != "net2" || a.PodRequest.Interface != "net2" {
		t.Errorf("a: %+v %+v", a.PodRequest, a.PodAttachment)
	}
	if b := byName(t, res.Networks, "b"); b.PodAttachment == nil || b.PodAttachment.Interface != "net1" {
		t.Errorf("b: %+v", b.PodAttachment)
	}
}

func TestCorrelateMultusDefaultNetwork(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{bridgeIface("data", "")},
			kubevirt.Network{Name: "data", Multus: &kubevirt.MultusNetwork{NetworkName: "infra/ovs", Default: true}},
		),
		Pod: multus.PodNetworking{
			StatusPresent: true,
			Statuses:      []multus.NetworkStatus{{Name: "infra/ovs", Interface: "eth0", Default: true}},
		},
	}
	d := byName(t, Correlate(in).Networks, "data")
	if !d.MultusDefault || d.PodRequest != nil {
		t.Errorf("default multus network should not have a pod request: %+v", d)
	}
	if d.PodAttachment == nil || d.PodAttachment.Interface != "eth0" {
		t.Errorf("attachment: %+v", d.PodAttachment)
	}
	if d.NAD.Ref.Namespace != "infra" || d.NAD.Lookup != kube.StateNotChecked {
		t.Errorf("nad: %+v", d.NAD)
	}
}

func TestCorrelateMissingLayers(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{bridgeIface("a", "")},
			multusNet("a", "br1"), multusNet("orphan", "bad/ref/x"),
		),
		Status: &kubevirt.VMIStatus{Interfaces: []kubevirt.InterfaceStatus{
			{Name: "stale", InterfaceName: "enp9s0"},
			{InterfaceName: "docker0", IP: "172.17.0.1"},
		}},
		NADs: map[multus.NetworkRef]NADResult{
			{Namespace: "demo", Name: "br1"}: {Lookup: kube.StateNotFound, Message: "not found"},
		},
	}
	res := Correlate(in)

	a := byName(t, res.Networks, "a")
	if a.Guest != nil || a.PodRequest != nil || a.PodAttachment != nil {
		t.Errorf("a should have no observed runtime layers: %+v", a)
	}
	if a.NAD.Lookup != kube.StateNotFound || a.NAD.Config != nil {
		t.Errorf("a nad: %+v", a.NAD)
	}
	o := byName(t, res.Networks, "orphan")
	if o.Interface != nil || o.NAD.RefError == "" {
		t.Errorf("orphan: %+v %+v", o.Interface, o.NAD)
	}
	if len(res.UnmatchedGuestInterfaces) != 1 || res.UnmatchedGuestInterfaces[0].Name != "stale" {
		t.Errorf("unmatched: %+v", res.UnmatchedGuestInterfaces)
	}
	if len(res.GuestOnlyInterfaces) != 1 || res.GuestOnlyInterfaces[0].InterfaceName != "docker0" {
		t.Errorf("guest-only: %+v", res.GuestOnlyInterfaces)
	}
}

func TestCorrelateNADConfigStates(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{bridgeIface("bad", ""), bridgeIface("file", "")},
			multusNet("bad", "bad"), multusNet("file", "file"),
		),
		NADs: map[multus.NetworkRef]NADResult{
			{Namespace: "demo", Name: "bad"}:  found("bad", `{"plugins":[{}]}`),
			{Namespace: "demo", Name: "file"}: found("file", ""),
		},
	}
	res := Correlate(in)
	if b := byName(t, res.Networks, "bad"); b.NAD.ConfigError == "" || b.NAD.Config != nil {
		t.Errorf("bad: %+v", b.NAD)
	}
	if f := byName(t, res.Networks, "file"); !f.NAD.ConfigNotInline || f.NAD.ConfigError != "" {
		t.Errorf("file: %+v", f.NAD)
	}
}

func TestCorrelateRecordsMatchMethod(t *testing.T) {
	in := CorrelationInput{
		Namespace: "demo",
		Spec: spec(
			[]kubevirt.Interface{{Name: "default", Masquerade: &marker}, bridgeIface("hinted", ""), bridgeIface("requested", ""), bridgeIface("ordered", "")},
			kubevirt.Network{Name: "default", Pod: &kubevirt.PodNetwork{}},
			multusNet("hinted", "a"), multusNet("requested", "b"), multusNet("ordered", "c"),
		),
		Status: &kubevirt.VMIStatus{Interfaces: []kubevirt.InterfaceStatus{{Name: "hinted", PodInterfaceName: "podA"}}},
		Pod: multus.PodNetworking{
			Selections: []multus.NetworkSelection{
				{Name: "a", Interface: "podA"}, {Name: "b", Interface: "podB"}, {Name: "c"},
			},
			StatusPresent: true,
			Statuses: []multus.NetworkStatus{
				{Name: "k8s-pod-network", Interface: "eth0", Default: true},
				{Name: "demo/a", Interface: "podA"},
				{Name: "demo/b", Interface: "podB"},
				{Name: "demo/c", Interface: "net3"},
				{Name: "demo/stray", Interface: "net9"},
			},
		},
	}
	res := Correlate(in)
	for name, want := range map[string]string{
		"default":   MatchDefaultFlag,
		"hinted":    MatchPodInterfaceName,
		"requested": MatchRequestInterface,
		"ordered":   MatchReferenceOrder,
	} {
		if got := byName(t, res.Networks, name).PodAttachment; got == nil || got.MatchedBy != want {
			t.Errorf("%s: attachment %+v, want matchedBy %s", name, got, want)
		}
	}
	if len(res.UnmatchedPodAttachments) != 1 || res.UnmatchedPodAttachments[0].NetworkName != "demo/stray" {
		t.Errorf("unmatched pod attachments: %+v", res.UnmatchedPodAttachments)
	}
}
