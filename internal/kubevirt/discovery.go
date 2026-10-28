package kubevirt

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

// GetVirtualMachine fetches a VirtualMachine.
func GetVirtualMachine(ctx context.Context, c dynamic.Interface, namespace, name string) (*VirtualMachine, error) {
	vm := &VirtualMachine{}
	if err := get(ctx, c, kube.VirtualMachineGVR, namespace, name, vm); err != nil {
		return nil, err
	}
	return vm, nil
}

// GetVirtualMachineInstance fetches a VirtualMachineInstance.
func GetVirtualMachineInstance(ctx context.Context, c dynamic.Interface, namespace, name string) (*VirtualMachineInstance, error) {
	vmi := &VirtualMachineInstance{}
	if err := get(ctx, c, kube.VirtualMachineInstanceGVR, namespace, name, vmi); err != nil {
		return nil, err
	}
	return vmi, nil
}

func get(ctx context.Context, c dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string, into any) error {
	u, err := c.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return decode(u, into)
}

func decode(u *unstructured.Unstructured, into any) error {
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into); err != nil {
		return fmt.Errorf("decoding %s %s/%s: %w", u.GetKind(), u.GetNamespace(), u.GetName(), err)
	}
	return nil
}

// ListLauncherPods returns the virt-launcher pods created for the VMI.
//
// Pods are selected by the kubevirt.io/created-by label (the VMI UID) and then
// confirmed through their controller owner reference, so a pod merely
// carrying a copied label is never attributed to the VMI. The result is
// sorted by creation time, oldest first.
func ListLauncherPods(ctx context.Context, c kubernetes.Interface, vmi *VirtualMachineInstance) ([]corev1.Pod, error) {
	selector := labels.SelectorFromSet(labels.Set{LabelCreatedBy: string(vmi.UID)})
	list, err := c.CoreV1().Pods(vmi.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	for _, p := range list.Items {
		if ownedBy(p.OwnerReferences, vmi.UID) {
			pods = append(pods, p)
		}
	}
	sort.SliceStable(pods, func(i, j int) bool {
		ti, tj := pods[i].CreationTimestamp, pods[j].CreationTimestamp
		if ti.Equal(&tj) {
			return pods[i].Name < pods[j].Name
		}
		return ti.Before(&tj)
	})
	return pods, nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid && r.Kind == "VirtualMachineInstance" {
			return true
		}
	}
	return false
}

// LauncherSelection is the outcome of choosing the active launcher pod.
type LauncherSelection struct {
	// Selected is the pod currently hosting the VMI, when it can be
	// determined unambiguously.
	Selected *corev1.Pod
	// Candidates are the non-terminated launcher pods considered.
	Candidates []corev1.Pod
	// Terminated are launcher pods that have completed (for example the
	// source pod of a finished migration).
	Terminated []corev1.Pod
	// Reason explains how the pod was selected or why none could be.
	Reason string
}

// SelectLauncherPod picks the launcher pod that currently hosts the VMI.
//
// Terminated pods are ignored. If VMI status lists active pods, only those are
// considered. When several remain (typically during live migration), the pod
// scheduled on the VMI's reported node is chosen; otherwise the selection is
// left ambiguous rather than guessed.
func SelectLauncherPod(vmi *VirtualMachineInstance, pods []corev1.Pod) LauncherSelection {
	var sel LauncherSelection
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			sel.Terminated = append(sel.Terminated, p)
			continue
		}
		sel.Candidates = append(sel.Candidates, p)
	}

	candidates := sel.Candidates
	if len(vmi.Status.ActivePods) > 0 && len(candidates) > 1 {
		var active []corev1.Pod
		for _, p := range candidates {
			if _, ok := vmi.Status.ActivePods[p.UID]; ok {
				active = append(active, p)
			}
		}
		if len(active) > 0 {
			candidates = active
		}
	}

	switch {
	case len(candidates) == 0:
		sel.Reason = "no non-terminated virt-launcher pod owned by the VMI was found"
		return sel
	case len(candidates) == 1:
		sel.Selected = &candidates[0]
		sel.Reason = "single active virt-launcher pod owned by the VMI"
		return sel
	}

	if node := vmi.Status.NodeName; node != "" {
		var onNode []corev1.Pod
		for _, p := range candidates {
			if p.Spec.NodeName == node {
				onNode = append(onNode, p)
			}
		}
		if len(onNode) == 1 {
			sel.Selected = &onNode[0]
			sel.Reason = fmt.Sprintf("%d active virt-launcher pods; selected the one on VMI node %q", len(candidates), node)
			return sel
		}
	}
	sel.Reason = fmt.Sprintf("%d active virt-launcher pods and none could be uniquely matched to the VMI node", len(candidates))
	return sel
}

// OwnerVMName returns the name of the VirtualMachine controlling the VMI, or
// "" if the VMI is not owned by a VirtualMachine.
func (vmi *VirtualMachineInstance) OwnerVMName() string {
	for _, r := range vmi.OwnerReferences {
		if r.Kind == "VirtualMachine" && r.Controller != nil && *r.Controller {
			return r.Name
		}
	}
	return ""
}
