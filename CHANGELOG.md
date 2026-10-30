# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the
major version is 0, minor releases may contain breaking changes to the CLI or the JSON
report schema; such changes are called out here.

## [Unreleased]

### Added

- `kubectl netdoctor topology`: graph of the VM network path with every relationship
  labeled Observed, Declared or Inferred, plus explicit gaps and absences.
- `kubectl netdoctor diagnose`: all rules, with `--host-snapshot`, `--expectations` and
  `--destination` (configuration and route analysis only; no traffic is sent).
- `kubectl netdoctor host collect` (experimental): opt-in, read-only node collector for
  Linux links, addresses, routes, bridge VLANs, bonds (including LACP partners) and OVS
  bridges, ports, interfaces, bonds, LACP and flow summaries.
- Host correlation: Linux bridge and macvlan parents, uplinks, VLAN sub-interfaces,
  Linux and OVS bonds, and OVS CNI ports identified by their `external_ids`.
- NAD parsing of IPAM subnets, gateways, routes and static addresses (host-local,
  static, whereabouts) and of VLAN trunk ranges.
- Attachment matching method (`matchedBy`) and unmatched Multus entries in the topology.
- Findings: `expected` field, and 20 new finding IDs for Multus MAC mismatches, host
  devices, OVS ports and flows, VLAN, MTU, bonds and LACP, routes and topology coverage.
- `docs/gap-analysis.md`: capability audit and comparison with existing tools.

- `ROUTE_UNDETERMINED` and `HOST_SNAPSHOT_HOSTNAME_MISMATCH`; `host collect` refuses a
  `--node-name` that does not match the host unless `--allow-node-name-mismatch` is set.
- Topology explains the launcher pod's cluster-network interface when the VMI does not
  use the pod network.

### Changed

- The text report says "No issues were found in the inspected state" (was "inspected
  API state").
- `diagnose.Evaluate` takes a `diagnose.Input` (internal API).
- Route analysis no longer reports `ROUTE_MISSING` when addressing is configured inside
  the guest; it reports `ROUTE_UNDETERMINED` instead.
- Route parsing accepts numeric protocol and table values from iproute2.

### v0.1 foundation

- `kubectl netdoctor inspect vm/<name>` and `inspect vmi/<name>` with text and JSON output.
- `kubectl netdoctor version`.
- Read-only discovery of VirtualMachines, VirtualMachineInstances, virt-launcher pods
  (label and owner-reference based), Multus `networks`/`network-status` annotations and
  NetworkAttachmentDefinitions.
- CNI configuration parsing for single plugins and plugin lists, with descriptions for
  common KubeVirt-related plugins (Linux bridge, OVS CNI, OVN-Kubernetes, SR-IOV,
  macvlan, Calico, ...).
- Per-network correlation of spec interface, guest interface status, launcher pod
  request/attachment and NAD.
- Diagnostic findings engine with Confirmed/Suspected/Unavailable categories and the rules
  documented in `docs/findings.md`.
- JSON report schema `netdoctor/v1alpha1`.
