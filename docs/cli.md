# CLI reference

`kubectl netdoctor` (the binary `kubectl-netdoctor`) has two kinds of commands:

- **Cluster commands** read the Kubernetes API with your kubeconfig. They are read-only
  and send no traffic. They accept the standard kubectl flags (`--kubeconfig`,
  `--context`, `-n/--namespace`, ...).
- **Host commands** (`host ...`) run on a node, started explicitly by an operator. They
  never contact the Kubernetes API.

Every command prints `--help` with examples.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | The command completed. With `--fail-on`, no finding reached the threshold. |
| `1` | The command could not run: bad arguments, unreadable input, API or kernel errors. |
| `2` | The report was written, and at least one finding reached `--fail-on`. |

`--fail-on none|info|warning|error` is available on `inspect`, `diagnose` and `report`.
The default `none` never returns 2.

## Cluster commands

| Command | Purpose | Key flags |
| --- | --- | --- |
| `inspect (vm\|vmi)/NAME` | API discovery and API-level findings | `-o text\|json`, `--fail-on`, `--timeout` |
| `topology (vm\|vmi)/NAME` | Graph of observed, declared and inferred relationships | `-o text\|json\|dot\|mermaid`, `--host-snapshot` |
| `diagnose (vm\|vmi)/NAME` | All rules | `--host-snapshot`, `--trace`, `--expectations`, `--destination IP`, `--fail-on` |
| `report (vm\|vmi)/NAME` | Diagnosis, topology and capabilities in one report or bundle | the diagnose flags, `--bundle FILE.tar.gz`, `--redact` |
| `trace (vm\|vmi)/NAME` | Writes a trace plan for the node tracer. Changes nothing. | `--duration` (≤10m), `--events drops,tcp,interfaces`, `-f FILE` |
| `capabilities` | API availability and RBAC (SelfSubjectAccessReview) for the namespace | `-o text\|json` |
| `version` | Build information | `-o json` |

### Bundles (`report --bundle`)

A `.tar.gz` with mode `0600` and these files:

- `manifest.json` (schema `netdoctor.bundle/v1alpha1`): SHA-256 and size of every file, plus notes on what is missing.
- `diagnosis.json` and `diagnosis.txt`.
- `topology.json`, `topology.txt`, `topology.dot` and `topology.mmd`.
- `capabilities.json`.
- Copies of the inputs under `inputs/`.

The same inputs produce the same archive.

`--redact` replaces IP and MAC addresses with placeholders such as `ip4-redacted-3` and `mac-2`:

- **Consistency:** the same address gets the same placeholder in every file and in the printed report.
- **Kept addresses:** loopback, unspecified and RFC 5737/3849 documentation addresses stay as they are.
- **Not redacted:** host, node, pod, namespace and object names. Review a bundle before sharing it.

## Host commands

| Command | Purpose | Privileges |
| --- | --- | --- |
| `host collect` | Read-only snapshot of links, addresses, routes, bridges, VLANs, bonds and OVS | Unprivileged for Linux state; root or the OVS socket group for OVS |
| `host trace` | eBPF trace of drops, TCP events and interface counters (see [ebpf.md](ebpf.md)) | root, or CAP_BPF + CAP_PERFMON (CAP_SYS_ADMIN for overhead stats) |
| `host capabilities` | Checks BTF, tracepoints, drop reasons, privileges and symbol visibility. By default it also loads the BPF programs through the verifier without attaching them. | root for `--load` |

`host trace` flags:

- `--plan FILE`: a plan from `trace`. The tracer finds the virt-launcher pod's network namespace on this node, filters on its interfaces and the VM addresses, and attributes each event.
- `--interface NAME` (repeatable), `--netns PATH`, `--address IP` (repeatable): explicit filters for use without a plan.
- `--unfiltered`: traces node-wide; events are Unattributed. It must be requested explicitly and cannot be combined with `--plan`.
- `--duration` (≤10m), `--events`, `--max-events` (default 10000), `--ring-bytes` (default 1 MiB, a power of two from 4 KiB to 64 MiB).
- `-f FILE`: writes the trace with mode 0600. A summary and any limitations go to stderr.
- `--node-name`, `--allow-node-name-mismatch`: the same node-name safety check as `host collect`.

Ctrl-C ends a trace early and still writes what was recorded.

## Typical workflows

```sh
# What can I see in this namespace?
kubectl netdoctor capabilities -n demo

# Configuration problems, failing a CI step on warnings
kubectl netdoctor diagnose vm/web-01 -n demo --fail-on warning

# Kernel evidence for a VM
kubectl netdoctor trace vm/web-01 -n demo --duration 60s -f plan.json
# on the VM's node, as root:
kubectl-netdoctor host capabilities
kubectl-netdoctor host trace --plan plan.json -f trace.json
# back on the workstation:
kubectl netdoctor diagnose vm/web-01 -n demo --trace trace.json

# Everything in one shareable file
kubectl netdoctor report vm/web-01 -n demo --host-snapshot node.json --trace trace.json \
  --bundle web-01.tar.gz --redact
```

## File formats

| Schema | Produced by |
| --- | --- |
| `netdoctor/v1alpha1` | `inspect`, `diagnose`, `topology`, `report`, `capabilities` |
| `netdoctor.host/v1alpha1` | `host collect` |
| `netdoctor.traceplan/v1alpha1` | `trace` |
| `netdoctor.trace/v1alpha1` | `host trace` |
| `netdoctor.bundle/v1alpha1` | `report --bundle` (manifest) |

All schemas are v1alpha1 and may change between minor versions.
