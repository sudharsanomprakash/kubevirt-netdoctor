package topology

import (
	"errors"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
)

// NADResult is the outcome of looking up one NetworkAttachmentDefinition.
type NADResult struct {
	NAD     *multus.NetworkAttachmentDefinition
	Lookup  kube.LookupState
	Message string
}

// CorrelationInput is everything the correlation needs. It is a pure data
// structure so correlation can be tested without an API server.
type CorrelationInput struct {
	Namespace string
	Spec      kubevirt.VMISpec
	// Status is nil when no VMI exists.
	Status *kubevirt.VMIStatus
	// Pod is the Multus view of the selected launcher pod; zero when no pod
	// was selected.
	Pod  multus.PodNetworking
	NADs map[multus.NetworkRef]NADResult
}

// CorrelationResult is the output of Correlate.
type CorrelationResult struct {
	Networks                 []NetworkPath
	UnmatchedGuestInterfaces []GuestInterface
	GuestOnlyInterfaces      []GuestInterface
	// UnmatchedPodAttachments are network-status entries not attributed to
	// any VMI network.
	UnmatchedPodAttachments []PodAttachment
}

// MultusRefs returns the distinct NADs referenced by the spec, in spec order.
// Malformed references are skipped; Correlate reports them per network.
func MultusRefs(spec kubevirt.VMISpec, namespace string) []multus.NetworkRef {
	seen := map[multus.NetworkRef]bool{}
	var refs []multus.NetworkRef
	for _, n := range spec.Networks {
		if n.Multus == nil {
			continue
		}
		ref, err := multus.ParseNetworkRef(n.Multus.NetworkName, namespace)
		if err != nil || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

// Correlate links each VMI network to its spec interface, guest interface
// status, launcher pod request and attachment, and NAD.
//
// Pod-level attachments are matched by the pod interface name whenever one is
// known (from the VMI status podInterfaceName or the Multus request), and
// otherwise by NAD reference in order of occurrence, so that two VMI networks
// using the same NAD are not conflated. The pod interface is never assumed to
// be the guest interface: they are recorded separately.
func Correlate(in CorrelationInput) CorrelationResult {
	var res CorrelationResult

	ifaces := map[string]kubevirt.Interface{}
	for _, i := range in.Spec.Domain.Devices.Interfaces {
		ifaces[i.Name] = i
	}
	statuses := map[string]kubevirt.InterfaceStatus{}
	if in.Status != nil {
		for _, s := range in.Status.Interfaces {
			if s.Name != "" {
				statuses[s.Name] = s
			}
		}
	}

	usedSelections := make([]bool, len(in.Pod.Selections))
	usedStatuses := make([]bool, len(in.Pod.Statuses))
	specNetworks := map[string]bool{}

	for _, n := range in.Spec.Networks {
		specNetworks[n.Name] = true
		path := NetworkPath{Name: n.Name, Source: SourceUnknown}

		if i, ok := ifaces[n.Name]; ok {
			path.Interface = &SpecInterface{Binding: i.BindingMethod(), MAC: i.MacAddress, Model: i.Model}
		}
		var podIfaceHint string
		if s, ok := statuses[n.Name]; ok {
			g := guestInterface(s)
			path.Guest = &g
			podIfaceHint = s.PodInterfaceName
		}

		switch {
		case n.Pod != nil:
			path.Source = SourcePod
			if idx := findStatus(in.Pod.Statuses, usedStatuses, func(s multus.NetworkStatus) bool { return s.Default }); idx >= 0 {
				path.PodAttachment = podAttachment(in.Pod.Statuses[idx], MatchDefaultFlag)
			}
		case n.Multus != nil:
			path.Source = SourceMultus
			path.MultusDefault = n.Multus.Default
			ref, err := multus.ParseNetworkRef(n.Multus.NetworkName, in.Namespace)
			if err != nil {
				path.NAD = &NADInfo{RefError: err.Error(), Lookup: kube.StateNotChecked}
				break
			}
			path.NAD = nadInfo(ref, in.NADs)
			correlatePodAttachment(&path, ref, n.Multus.Default, podIfaceHint, in, usedSelections, usedStatuses)
		}
		res.Networks = append(res.Networks, path)
	}

	for i, st := range in.Pod.Statuses {
		if !usedStatuses[i] {
			res.UnmatchedPodAttachments = append(res.UnmatchedPodAttachments, *podAttachment(st, ""))
		}
	}

	if in.Status != nil {
		for _, s := range in.Status.Interfaces {
			switch {
			case s.Name == "":
				res.GuestOnlyInterfaces = append(res.GuestOnlyInterfaces, guestInterface(s))
			case !specNetworks[s.Name]:
				res.UnmatchedGuestInterfaces = append(res.UnmatchedGuestInterfaces, guestInterface(s))
			}
		}
	}
	return res
}

func correlatePodAttachment(path *NetworkPath, ref multus.NetworkRef, isDefault bool, podIfaceHint string,
	in CorrelationInput, usedSelections, usedStatuses []bool) {
	matchesRef := func(s multus.NetworkStatus) bool { return s.MatchesRef(ref, in.Namespace) }

	if isDefault {
		// A Multus default network replaces the cluster network and is not
		// listed in the networks annotation.
		if idx := findStatus(in.Pod.Statuses, usedStatuses, matchesRef); idx >= 0 {
			path.PodAttachment = podAttachment(in.Pod.Statuses[idx], MatchDefaultFlag)
		}
		return
	}

	selIdx := -1
	for i, sel := range in.Pod.Selections {
		if usedSelections[i] || sel.Ref(in.Namespace) != ref {
			continue
		}
		if podIfaceHint != "" && sel.Interface != "" && sel.Interface != podIfaceHint {
			continue
		}
		selIdx = i
		break
	}
	podIface, method := podIfaceHint, MatchPodInterfaceName
	if selIdx >= 0 {
		usedSelections[selIdx] = true
		sel := in.Pod.Selections[selIdx]
		path.PodRequest = &PodRequest{Interface: sel.Interface, MAC: sel.MAC}
		if podIface == "" {
			podIface, method = sel.Interface, MatchRequestInterface
		}
	}

	var idx int
	if podIface != "" {
		idx = findStatus(in.Pod.Statuses, usedStatuses, func(s multus.NetworkStatus) bool {
			return matchesRef(s) && s.Interface == podIface
		})
	} else {
		idx, method = findStatus(in.Pod.Statuses, usedStatuses, matchesRef), MatchReferenceOrder
	}
	if idx >= 0 {
		path.PodAttachment = podAttachment(in.Pod.Statuses[idx], method)
	}
}

func findStatus(statuses []multus.NetworkStatus, used []bool, match func(multus.NetworkStatus) bool) int {
	for i, s := range statuses {
		if !used[i] && match(s) {
			used[i] = true
			return i
		}
	}
	return -1
}

func nadInfo(ref multus.NetworkRef, nads map[multus.NetworkRef]NADResult) *NADInfo {
	info := &NADInfo{Ref: ref, Lookup: kube.StateNotChecked}
	r, ok := nads[ref]
	if !ok {
		return info
	}
	info.Lookup, info.Message = r.Lookup, r.Message
	if r.NAD == nil {
		return info
	}
	info.ResourceName = r.NAD.ResourceName
	cfg, err := multus.ParseCNIConfig(r.NAD.Config)
	switch {
	case errors.Is(err, multus.ErrEmptyConfig):
		info.ConfigNotInline = true
	case err != nil:
		info.ConfigError = err.Error()
	default:
		info.Config = cfg
	}
	return info
}

func guestInterface(s kubevirt.InterfaceStatus) GuestInterface {
	return GuestInterface{
		Name:             s.Name,
		InterfaceName:    s.InterfaceName,
		MAC:              s.MAC,
		IPs:              s.AllIPs(),
		InfoSource:       s.InfoSource,
		PodInterfaceName: s.PodInterfaceName,
		LinkState:        s.LinkState,
	}
}

func podAttachment(s multus.NetworkStatus, method string) *PodAttachment {
	return &PodAttachment{NetworkName: s.Name, Interface: s.Interface, MAC: s.MAC, IPs: s.IPs, Default: s.Default, MatchedBy: method}
}
