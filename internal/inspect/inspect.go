// Package inspect performs the read-only Kubernetes API discovery for a VM or
// VMI and assembles the correlated topology.
package inspect

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kubevirt"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/multus"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// TargetError is returned when the inspected VM/VMI itself cannot be read.
// Without the target there is nothing to diagnose.
type TargetError struct {
	Target topology.Target
	State  kube.LookupState
	Err    error
}

func (e *TargetError) Error() string {
	switch e.State {
	case kube.StateNotFound:
		return fmt.Sprintf("%s %q not found in namespace %q", e.Target.Kind, e.Target.Name, e.Target.Namespace)
	case kube.StateForbidden:
		return fmt.Sprintf("not permitted to read %s %q in namespace %q (see the RBAC section of the README): %v",
			e.Target.Kind, e.Target.Name, e.Target.Namespace, e.Err)
	default:
		return fmt.Sprintf("reading %s %q in namespace %q: %v", e.Target.Kind, e.Target.Name, e.Target.Namespace, e.Err)
	}
}

func (e *TargetError) Unwrap() error { return e.Err }

// Inspect discovers the topology of the target VM or VMI.
//
// Only the target itself is mandatory. Failures reading related objects
// (VMI, launcher pods, NADs) are recorded in the topology as lookup states
// and access issues, so the diagnosis can say what could not be observed.
func Inspect(ctx context.Context, c *kube.Clients, target topology.Target) (*topology.Topology, error) {
	if err := checkKubeVirtAPI(ctx, c, target); err != nil {
		return nil, err
	}
	topo := &topology.Topology{Target: target}
	var (
		vm  *kubevirt.VirtualMachine
		vmi *kubevirt.VirtualMachineInstance
		err error
	)

	switch target.Kind {
	case topology.KindVM:
		vm, err = kubevirt.GetVirtualMachine(ctx, c.Dynamic, target.Namespace, target.Name)
		if err != nil {
			return nil, &TargetError{Target: target, State: kube.Classify(err), Err: err}
		}
		topo.VirtualMachine = vmInfo(vm)

		vmi, err = kubevirt.GetVirtualMachineInstance(ctx, c.Dynamic, target.Namespace, target.Name)
		if err != nil {
			topo.AccessIssues = appendIssue(topo.AccessIssues, "get", kube.VirtualMachineInstanceGVR, target.Namespace, target.Name, err)
			topo.Instance = &topology.VMIInfo{Name: target.Name, Lookup: kube.Classify(err), NetworkSpecSource: topology.SpecFromVMTemplate}
			vmi = nil
		}

	case topology.KindVMI:
		vmi, err = kubevirt.GetVirtualMachineInstance(ctx, c.Dynamic, target.Namespace, target.Name)
		if err != nil {
			return nil, &TargetError{Target: target, State: kube.Classify(err), Err: err}
		}
		if owner := vmi.OwnerVMName(); owner != "" {
			owned, err := kubevirt.GetVirtualMachine(ctx, c.Dynamic, target.Namespace, owner)
			if err != nil {
				topo.AccessIssues = appendIssue(topo.AccessIssues, "get", kube.VirtualMachineGVR, target.Namespace, owner, err)
				topo.VirtualMachine = &topology.VMInfo{Name: owner, Lookup: kube.Classify(err)}
			} else {
				topo.VirtualMachine = vmInfo(owned)
			}
		}

	default:
		return nil, fmt.Errorf("unsupported target kind %q", target.Kind)
	}

	var (
		spec   kubevirt.VMISpec
		status *kubevirt.VMIStatus
		pod    *corev1.Pod
	)
	if vmi != nil {
		spec, status = vmi.Spec, &vmi.Status
		topo.Instance = vmiInfo(vmi)
		topo.NodeName = vmi.Status.NodeName
		topo.Launcher, pod = discoverLauncher(ctx, c, vmi, &topo.AccessIssues)
	} else {
		topo.Launcher = &topology.LauncherInfo{Lookup: kube.StateNotChecked, SelectionReason: "no VirtualMachineInstance exists"}
		if vm != nil && vm.Spec.Template != nil {
			spec = vm.Spec.Template.Spec
		}
	}

	podNet := multus.ReadPodNetworking(pod)
	if pod != nil {
		topo.Launcher.NetworkStatusPresent = podNet.StatusPresent
		topo.Launcher.DefaultNetwork = podNet.DefaultNetwork
		if podNet.StatusErr != nil {
			topo.Launcher.NetworkStatusError = podNet.StatusErr.Error()
		}
		if podNet.SelectionsErr != nil {
			topo.Launcher.NetworksError = podNet.SelectionsErr.Error()
		}
	}

	res := topology.Correlate(topology.CorrelationInput{
		Namespace: target.Namespace,
		Spec:      spec,
		Status:    status,
		Pod:       podNet,
		NADs:      lookupNADs(ctx, c, topology.MultusRefs(spec, target.Namespace), &topo.AccessIssues),
	})
	topo.Networks = res.Networks
	topo.UnmatchedGuestInterfaces = res.UnmatchedGuestInterfaces
	topo.GuestOnlyInterfaces = res.GuestOnlyInterfaces
	topo.UnmatchedPodAttachments = res.UnmatchedPodAttachments
	return topo, nil
}

// checkKubeVirtAPI turns "KubeVirt is not installed" into a clear error.
// Discovery failures are ignored; the subsequent Get reports them.
func checkKubeVirtAPI(ctx context.Context, c *kube.Clients, target topology.Target) error {
	gvr := kube.VirtualMachineInstanceGVR
	if target.Kind == topology.KindVM {
		gvr = kube.VirtualMachineGVR
	}
	served, err := kube.APIServed(ctx, c, gvr)
	if err == nil && !served {
		return &TargetError{
			Target: target,
			State:  kube.StateUnavailable,
			Err:    fmt.Errorf("the cluster does not serve %s; is KubeVirt installed?", kube.ResourceString(gvr)),
		}
	}
	return nil
}

func discoverLauncher(ctx context.Context, c *kube.Clients, vmi *kubevirt.VirtualMachineInstance,
	issues *[]kube.AccessIssue) (*topology.LauncherInfo, *corev1.Pod) {
	pods, err := kubevirt.ListLauncherPods(ctx, c.Core, vmi)
	if err != nil {
		*issues = appendIssue(*issues, "list", kube.PodGVR, vmi.Namespace, "", err)
		return &topology.LauncherInfo{Lookup: kube.Classify(err), SelectionReason: "virt-launcher pods could not be listed"}, nil
	}
	sel := kubevirt.SelectLauncherPod(vmi, pods)
	info := &topology.LauncherInfo{
		Lookup:          kube.StateFound,
		SelectionReason: sel.Reason,
		Candidates:      podInfos(sel.Candidates),
		Terminated:      podInfos(sel.Terminated),
	}
	if len(sel.Candidates) == 0 {
		info.Lookup = kube.StateNotFound
	}
	if sel.Selected != nil {
		p := podInfo(*sel.Selected)
		info.Selected = &p
	}
	return info, sel.Selected
}

func lookupNADs(ctx context.Context, c *kube.Clients, refs []multus.NetworkRef,
	issues *[]kube.AccessIssue) map[multus.NetworkRef]topology.NADResult {
	out := map[multus.NetworkRef]topology.NADResult{}
	if len(refs) == 0 {
		return out
	}
	gvr := kube.NetworkAttachmentDefinitionGVR
	if served, err := kube.APIServed(ctx, c, gvr); err == nil && !served {
		msg := fmt.Sprintf("the cluster does not serve %s; Multus CNI may not be installed", kube.ResourceString(gvr))
		*issues = append(*issues, kube.AccessIssue{
			Verb: "discover", Resource: kube.ResourceString(gvr), State: kube.StateUnavailable, Message: msg,
		})
		for _, ref := range refs {
			out[ref] = topology.NADResult{Lookup: kube.StateUnavailable, Message: msg}
		}
		return out
	}
	for _, ref := range refs {
		nad, err := multus.GetNAD(ctx, c.Dynamic, ref)
		if err != nil {
			state := kube.Classify(err)
			if state != kube.StateNotFound {
				*issues = appendIssue(*issues, "get", gvr, ref.Namespace, ref.Name, err)
			}
			out[ref] = topology.NADResult{Lookup: state, Message: err.Error()}
			continue
		}
		out[ref] = topology.NADResult{NAD: nad, Lookup: kube.StateFound}
	}
	return out
}

// appendIssue records a failed request unless it is a plain NotFound, which
// callers report through lookup states instead.
func appendIssue(issues []kube.AccessIssue, verb string, gvr schema.GroupVersionResource, ns, name string, err error) []kube.AccessIssue {
	if kube.Classify(err) == kube.StateNotFound {
		return issues
	}
	return append(issues, kube.NewAccessIssue(verb, gvr, ns, name, err))
}

func vmInfo(vm *kubevirt.VirtualMachine) *topology.VMInfo {
	return &topology.VMInfo{
		Name:            vm.Name,
		Lookup:          kube.StateFound,
		RunStrategy:     vm.EffectiveRunStrategy(),
		PrintableStatus: vm.Status.PrintableStatus,
		Ready:           vm.Status.Ready,
	}
}

func vmiInfo(vmi *kubevirt.VirtualMachineInstance) *topology.VMIInfo {
	agent := topology.AgentUnknown
	if c, ok := kubevirt.FindCondition(vmi.Status.Conditions, kubevirt.ConditionAgentConnected); ok {
		if c.Status == string(corev1.ConditionTrue) {
			agent = topology.AgentConnected
		} else {
			agent = topology.AgentNotConnected
		}
	} else if vmi.Status.Phase == kubevirt.PhaseRunning {
		// KubeVirt only sets AgentConnected once an agent has connected.
		agent = topology.AgentNotConnected
	}
	return &topology.VMIInfo{
		Name:                vmi.Name,
		Lookup:              kube.StateFound,
		UID:                 string(vmi.UID),
		Phase:               vmi.Status.Phase,
		Reason:              vmi.Status.Reason,
		NodeName:            vmi.Status.NodeName,
		GuestAgent:          agent,
		MigrationInProgress: vmi.Status.MigrationState.InProgress(),
		Conditions:          vmi.Status.Conditions,
		NetworkSpecSource:   topology.SpecFromVMI,
	}
}

func podInfos(pods []corev1.Pod) []topology.PodInfo {
	out := make([]topology.PodInfo, 0, len(pods))
	for _, p := range pods {
		out = append(out, podInfo(p))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func podInfo(p corev1.Pod) topology.PodInfo {
	info := topology.PodInfo{Name: p.Name, UID: string(p.UID), Phase: string(p.Status.Phase), NodeName: p.Spec.NodeName}
	for _, ip := range p.Status.PodIPs {
		info.PodIPs = append(info.PodIPs, ip.IP)
	}
	return info
}
