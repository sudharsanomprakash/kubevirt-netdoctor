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

- netdoctor is **read-only**: it issues only `get` and `list` requests to the Kubernetes API.
- It runs with the credentials of the user's kubeconfig; it does not create service accounts,
  pods, DaemonSets or any other objects.
- It does not execute commands in pods or on nodes and does not send network traffic to
  workloads.
- Output can contain cluster object names, IP and MAC addresses. Review it before sharing.
