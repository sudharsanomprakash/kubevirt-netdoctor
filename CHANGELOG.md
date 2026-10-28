# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the
major version is 0, minor releases may contain breaking changes to the CLI or the JSON
report schema; such changes are called out here.

## [Unreleased]

### Added

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
