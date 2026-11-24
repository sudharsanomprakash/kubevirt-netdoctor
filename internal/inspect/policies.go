package inspect

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// lookupNetworkPolicies lists the namespace's policies and reports which ones
// select the launcher pod. It makes no claim about whether traffic is allowed:
// enforcement belongs to the CNI.
func lookupNetworkPolicies(
	ctx context.Context,
	c *kube.Clients,
	namespace string,
	pod *corev1.Pod,
	issues *[]kube.AccessIssue,
) []topology.NetworkPolicyInfo {
	list, err := c.Core.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		*issues = appendIssue(*issues, "list", kube.NetworkPolicyGVR, namespace, "", err)
		return nil
	}
	out := make([]topology.NetworkPolicyInfo, 0, len(list.Items))
	for _, policy := range list.Items {
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			continue
		}
		types := make([]string, 0, len(policy.Spec.PolicyTypes))
		for _, t := range policy.Spec.PolicyTypes {
			types = append(types, string(t))
		}
		out = append(out, topology.NetworkPolicyInfo{
			Name:            policy.Name,
			PolicyTypes:     types,
			IngressRules:    len(policy.Spec.Ingress),
			EgressRules:     len(policy.Spec.Egress),
			SelectsLauncher: selector.Matches(labels.Set(pod.Labels)),
		})
	}
	return out
}
