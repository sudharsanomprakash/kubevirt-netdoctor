# KubeVirt lab for end-to-end tests

A disposable single-node lab that exercises netdoctor against real KubeVirt VMs on
real VLAN networking. Do not run any of this against a cluster you care about: the
test runner deliberately breaks the lab network (and repairs it).

## What it builds

- **Host:** Ubuntu 24.04 VM with nested virtualization (`/dev/kvm`): 8 vCPU, 32 GB RAM,
  100 GB disk.
- **Kubernetes:**
  - kubeadm with containerd and flannel;
  - Multus, the Linux bridge CNI and OVS CNI (through cluster-network-addons-operator);
  - KubeVirt (latest stable).
- **Network:** a network namespace, `upstream`, stands in for the physical switch and
  router.
  - `br1` is a VLAN-filtering Linux bridge. Its uplink `bond0` is an active-backup bond
    of two members, trunking VLAN 100.
  - `br-ovs` is an OVS bridge. Its uplink `veth-o` trunks VLAN 200.
  - The upstream gateways are 192.168.100.1 and 192.168.200.1.
- **Workloads** (namespace `lab`):

  | Name | Kind | Network |
  | --- | --- | --- |
  | `vm-masq` | VM | Pod network, masquerade |
  | `vm-bridge` | VM | Linux bridge on VLAN 100, 192.168.100.10 |
  | `vm-ovs` | VM | OVS on VLAN 200, 192.168.200.10 |
  | `app-vlan100` | App pod | VLAN 100, 192.168.100.20 |
  | `app-vlan200` | App pod | VLAN 200, 192.168.200.20 |

## Quick start (unattended)

From Cloud Shell, or any machine with `gcloud` signed in:

```sh
git clone -b test/kubevirt-lab https://github.com/sudharsanomprakash/kubevirt-netdoctor
cd kubevirt-netdoctor
hack/lab/create-vm.sh auto        # creates the VM; it builds the lab and runs the tests by itself
hack/lab/create-vm.sh results     # progress, or the summary when finished (about 30-45 minutes)
hack/lab/create-vm.sh stop        # stop billing when done
```

On the VM, `sudo nohup hack/lab/run-all.sh >/var/log/netdoctor-lab.log 2>&1 &` re-runs
everything. It is idempotent and resumes after a fix.

## Steps (manual)

On your workstation, with `gcloud` signed in:

```sh
hack/lab/create-vm.sh create      # VM with nested virtualization
hack/lab/create-vm.sh copy        # copy this repository to it
hack/lab/create-vm.sh ssh
```

On the lab VM:

```sh
cd kubevirt-netdoctor
sudo hack/lab/setup.sh            # about 15-25 minutes
sudo hack/lab/workloads.sh        # deploys VMs/pods and checks the lab network works
sudo hack/lab/run-tests.sh        # scenarios; results in ./lab-results/<timestamp>/
```

When you are done:

```sh
hack/lab/create-vm.sh stop        # stops CPU/RAM billing; the disk is still billed
hack/lab/create-vm.sh delete      # removes everything
```

After a reboot of the lab VM, run `sudo hack/lab/setup.sh network` again. The simulated
switch, bond and bridges are not persistent.

## Compatibility notes (found on the first live run)

- **KubeVirt manifests need server-side apply.** The `kubevirts.kubevirt.io` CRD exceeds
  the 256 KiB annotation limit of client-side `kubectl apply`.
- **KubeVirt v1.10 mounts container disks as Kubernetes image volumes.** This needs:
  - containerd 2.1 or newer (Ubuntu 24.04 ships 1.7, so `setup.sh` installs `containerd.io`
    from Docker's repository);
  - on Kubernetes 1.34 and older, the `ImageVolume` feature gate on the API server and the
    kubelet (`setup.sh` enables it).

  Without these, VMs fail with `failed to find disk file from ImageVolume`.

## Scenarios

| | Fault (lab only) | Expected |
| --- | --- | --- |
| A | none | No Error findings on any VM; capabilities; tracing Available; DOT/Mermaid |
| B | VM references a NAD that does not exist | `NAD_NOT_FOUND` |
| C | VLAN 100 removed from the bridge uplink | `VLAN_MISMATCH` |
| D | VLAN filtering disabled on `br1` | `VLAN_NOT_ENFORCED` |
| E | OVS port of `vm-ovs` re-tagged to VLAN 999 | `VLAN_MISMATCH` |
| F | `br1` down | `HOST_INTERFACE_DOWN` |
| G | bond member detached | `BOND_MEMBER_DETACHED` |
| H | 9000 MTU expected, 1500 on the host | `MTU_MISMATCH` |
| I | none: trace of the live virt-launcher pod | Launcher netns found from the pod cgroup; packets counted on its interfaces; `diagnose --trace` uses the trace |
| J | uplink VLAN removed during a trace | Records which kernel drops are observed (informational) |
| K | none | Redacted `report` bundle contains no VM addresses |

After every fault the runner reverts it and re-checks the baseline.

## What the tracer can and cannot see for VMs

The TCP hooks (retransmit, reset, state) fire only for connections terminated by the
**host** kernel, such as application pods. A VM's TCP stack runs in the guest kernel,
and its packets cross the host only through the tap, bridge or NAT path. For VM traffic
the tracer therefore observes:

- host-path drops;
- per-interface packet counts in the launcher pod.

It does not observe guest retransmissions or resets. Scenario I asserts only what the
host can see.
