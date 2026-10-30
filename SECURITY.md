# Security Policy

## Supported versions

kubevirt-netdoctor is in early development (v0.x). Security fixes are made on the `main`
branch and released in the next patch or minor version. Only the latest release is supported.

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/sudharsanomprakash/kubevirt-netdoctor/security/advisories/new)
for this repository. Include:

- a description of the issue and its impact,
- steps to reproduce (with synthetic data only),
- the affected version (`kubectl netdoctor version`).

You can expect an acknowledgement within 7 days. We will coordinate a fix and disclosure
timeline with you and credit you in the advisory unless you prefer otherwise.

## Security model

- The cluster commands (`inspect`, `topology`, `diagnose`) are **read-only**: they issue
  only `get` and `list` requests to the Kubernetes API with the credentials of the user's
  kubeconfig. They do not create service accounts, pods, DaemonSets or any other objects,
  do not execute commands in pods or on nodes, and never send network traffic.
- `host collect` (experimental) runs only when an operator starts it on a node. It
  executes a fixed allowlist of read-only `ip`, `bridge`, `ovs-vsctl`, `ovs-appctl` and
  `ovs-ofctl` subcommands directly (never through a shell), with per-command timeouts,
  reads `/proc/net/bonding`, and passes no user input to any command. It does not contact
  the Kubernetes API. See the README's host collector section for required privileges.
- netdoctor never installs or deploys privileged components.
- Output and host snapshots can contain object names, pod UIDs, IP and MAC addresses and
  routes. Snapshot files are written with mode 0600. Review them before sharing.
