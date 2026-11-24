# Compatibility matrix

What netdoctor has been run against, and what it models. Everything here is
backed by a test or a lab run in this repository; nothing is projected.

## Validated in the lab

The live run is described in
[implementation-status.md](implementation-status.md#live-lab-validation): a GCE
`n2-standard-8` with nested virtualization, three CirrOS VMs (masquerade, Linux
bridge VLAN 100, OVS VLAN 200), app pods on both VLANs, a bonded VLAN trunk and
a simulated upstream switch. All 43 checks passed.

| Component | Version |
| --- | --- |
| Kubernetes | v1.33.13 (kubeadm) |
| KubeVirt | v1.10.0 |
| cluster-network-addons (Multus, Linux bridge CNI, OVS CNI) | v0.104.0-rc-0 |
| containerd | 2.x |
| Kernel | 7.0.0-1017-gcp |

The primary network was validated with Calico.

## CNI support levels

The per-plugin detail, including which host checks exist, is in the
[CNI support matrix](cni-support.md). In short:

| Level | CNI plugins |
| --- | --- |
| Host path modeled (API checks plus host snapshot correlation) | Linux bridge (`bridge`, `cnv-bridge`), OVS CNI (`ovs`), macvlan / ipvlan |
| API level only (no host-level claims) | OVN-Kubernetes secondary (`ovn-k8s-cni-overlay`), Kube-OVN (`kube-ovn`), SR-IOV, host-device, and ptp / Calico / Cilium / Flannel as secondary plugins |
| Recognized in plugin chains | Meta plugins: `tuning`, `cnv-tuning`, `portmap`, `bandwidth`, `sbr`, `firewall`, `vrf` |

Any other plugin type is parsed generically, labeled "unrecognized plugin type",
and still gets the API-level checks. The primary CNI is treated generically.

## Kernel and privilege requirements

From [ebpf.md](ebpf.md#portability), for the experimental kernel tracer:

| Requirement | Needed for |
| --- | --- |
| Linux with BTF (`/sys/kernel/btf/vmlinux`) | Loading any program (CO-RE relocation) |
| Ring buffer support (Linux 5.8+) | Reading events |
| Linux 5.17+ | Drop reasons; older kernels report drops without one |
| CAP_BPF and CAP_PERFMON, or root | Loading and attaching programs |
| CAP_SYS_ADMIN | `BPF_ENABLE_STATS`, used to measure overhead |

A missing tracepoint is skipped and reported as `TRACE_HOOK_UNAVAILABLE` rather
than failing the whole trace.

## Not covered

Userspace and DPDK datapaths, guest-internal networking, physical switch state,
and CNI-specific internals (Calico/Cilium policy and BGP, OVN logical topology,
OVN-Kubernetes user-defined networks). The host collector reads what the node
exposes; it does not query these systems.
