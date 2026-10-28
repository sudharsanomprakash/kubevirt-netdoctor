package multus

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// NetworkAttachmentDefinition is the subset of a k8s.cni.cncf.io/v1 NAD read
// by netdoctor.
type NetworkAttachmentDefinition struct {
	Namespace string
	Name      string
	// ResourceName is the device-plugin resource requested for the
	// attachment (for example an SR-IOV resource), if annotated.
	ResourceName string
	// Config is the raw spec.config string.
	Config string
}

// GetNAD fetches a NetworkAttachmentDefinition.
func GetNAD(ctx context.Context, c dynamic.Interface, ref NetworkRef) (*NetworkAttachmentDefinition, error) {
	u, err := c.Resource(kube.NetworkAttachmentDefinitionGVR).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	cfg, _, err := unstructured.NestedString(u.Object, "spec", "config")
	if err != nil {
		return nil, fmt.Errorf("reading spec.config of NetworkAttachmentDefinition %s: %w", ref, err)
	}
	return &NetworkAttachmentDefinition{
		Namespace:    u.GetNamespace(),
		Name:         u.GetName(),
		ResourceName: u.GetAnnotations()[ResourceNameAnnotation],
		Config:       cfg,
	}, nil
}
