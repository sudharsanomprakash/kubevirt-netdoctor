package multus

import corev1 "k8s.io/api/core/v1"

// PodNetworking is the Multus view of a pod: the attachments it requested and
// the attachments Multus reported as set up.
type PodNetworking struct {
	Selections    []NetworkSelection
	SelectionsErr error
	// StatusPresent is false when the network-status annotation is absent,
	// in which case attachment state cannot be confirmed either way.
	StatusPresent bool
	Statuses      []NetworkStatus
	StatusErr     error
	// DefaultNetwork is the value of the Multus default-network override
	// annotation, if set.
	DefaultNetwork string
}

// ReadPodNetworking extracts Multus annotations from a pod. A nil pod yields
// an empty result.
func ReadPodNetworking(pod *corev1.Pod) PodNetworking {
	var pn PodNetworking
	if pod == nil {
		return pn
	}
	ann := pod.Annotations
	pn.Selections, pn.SelectionsErr = ParseNetworkSelections(ann[NetworksAnnotation])
	if v, ok := ann[NetworkStatusAnnotation]; ok {
		pn.StatusPresent = true
		pn.Statuses, pn.StatusErr = ParseNetworkStatus(v)
	}
	pn.DefaultNetwork = ann[DefaultNetworkAnnotation]
	return pn
}
