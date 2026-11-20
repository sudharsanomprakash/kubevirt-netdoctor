# Architecture: future diagnostic and remediation agents

Status: **design constraints only**. No agent and no remediation code exists. This
document records the extension points the current code keeps open, and the rules
any future agent work must follow. It does not commit to a schedule.

## Principles

1. **The diagnostic engine is read-only and stays separate from execution.**
   Packages that observe and reason (`kube`, `inspect`, `topology`, `host`,
   `hostpath`, `route`, `ebpf`, `traceplan`, `diagnose`, `graph`) never import
   anything that changes cluster or node state. A remediation subsystem may
   import the engine's types; the engine must never import remediation.
2. **Enabling an agent is not authorizing a change.** Running an agent and
   allowing it to modify infrastructure are two separate controls, owned by
   possibly different people, and evaluated separately on every action.
3. **No general-purpose power.** Agents never get a shell, arbitrary command
   execution, `cluster-admin`, or wildcard RBAC. Every change is a typed
   action with a fixed, minimal privilege set.
4. **Evidence first.** An action can only be proposed from a finding with a
   stable ID and evidence. Speculative or `Unavailable` findings cannot trigger
   an action.

## Agent types

| Agent | What it does | Privileges |
| --- | --- | --- |
| Diagnostic agent | Runs the existing engine continuously or on demand (API inspection, host collection, bounded eBPF traces) and publishes findings | Read-only RBAC (same verbs `capabilities` checks); node-side: the privileges `host collect` / `host trace` need, nothing more |
| Remediation agent | Turns approved findings into typed actions and executes them | Per action type, declared up front; never broader than the action needs |

The two are separate binaries/processes with separate service accounts, so a
compromise of the diagnostic side yields no write access.

## Controls

Three scopes, evaluated most specific first, where an explicit Disabled at any
scope wins:

- **Cluster**: global on/off per agent, and the default remediation mode.
- **Node**: opt in/out per node (label or selector), e.g. exclude control-plane
  nodes.
- **Workload**: per namespace / VM (label or annotation), e.g. never touch a
  production VM.

Per scope, two independent settings:

- `agentEnabled` (may the agent run and observe here)
- `remediationMode` (may it change anything here), one of:

| Mode | Behavior |
| --- | --- |
| `Disabled` | No actions are planned or executed. Default. |
| `Observe` | Actions are planned and recorded ("would have done X"), never executed. |
| `ApprovalRequired` | Each action waits for an explicit human approval bound to that exact action and its preconditions. |
| `PolicyAutomatic` | Actions that a policy explicitly allows run without a human; anything not allowed falls back to `ApprovalRequired`. |

## Typed remediation actions (extension point)

A future `internal/remediation` package (not created yet) defines actions as
data plus a fixed executor, roughly:

```go
type Action interface {
	Type() string                 // stable ID, e.g. "SetNADMTU"
	Target() Ref                  // the one object or node it changes
	FromFinding() string          // finding ID that justifies it, e.g. "MTU_MISMATCH"
	RequiredPermissions() []Permission // declared, checked before execution
	Preconditions(ctx) error      // re-read live state; refuse if it drifted
	Plan() Diff                   // exact intended change, shown to approvers
	Execute(ctx) (Result, error)  // narrowly privileged
	Verify(ctx) error             // re-run the originating diagnostic rule
	Rollback(ctx) error           // ErrNoRollback when not supported
}
```

- Actions are registered in code, never loaded as scripts.
- `Plan` is computed before approval; execution re-checks `Preconditions` and
  aborts if the live state no longer matches what was approved.
- `Verify` re-runs the same `diagnose` rule that produced the finding; success
  means the finding is gone, not that the command returned 0.
- Actions without safe rollback say so in `Plan`, and policy can forbid them
  from `PolicyAutomatic`.

## Policy evaluation (extension point)

Policy is evaluated by a component separate from both the planner and the
executor: `(action, target, scope settings, policy) -> Allow | RequireApproval | Deny`
with the reason recorded. Inputs are the typed action and the finding; policy
never sees free-form commands. Rate limits and blast-radius limits (max
actions per node/hour, never two nodes at once) belong here.

## Approval, audit, management

- **Approval** records who approved, when, and a hash of the exact plan and
  preconditions; any change invalidates it.
- **Audit trail**: every planned, approved, denied, executed, verified and
  rolled-back action is an append-only record with the finding evidence.
  Stored as Kubernetes objects (CRDs or Events) so existing RBAC and retention
  apply.
- **Management surfaces** (CLI, API, web UI) all operate on the same objects:
  agent configuration, pending approvals, audit records. No surface has a
  privileged side channel.

## Seams that exist today

| Seam | Where | Why it matters |
| --- | --- | --- |
| Stable finding IDs, categories, evidence, expected state | `internal/diagnose/finding.go`, `docs/findings.md` | The only allowed trigger for an action; `Confirmed` vs `Suspected` vs `Unavailable` gates eligibility |
| Rules as pure functions over `diagnose.Input` | `internal/diagnose` | `Verify` can re-run a single rule after a change |
| Versioned JSON schemas (`netdoctor/v1alpha1`, `netdoctor.host/v1alpha1`, `netdoctor.trace/v1alpha1`, `netdoctor.traceplan/v1alpha1`) | `internal/output`, `internal/host`, `internal/ebpf`, `internal/traceplan` | A diagnostic agent can publish exactly what the CLI prints |
| Capability checks (RBAC via SelfSubjectAccessReview, node privileges) | `internal/capability`, `internal/ebpf/caps*.go` | The same mechanism verifies an action's declared permissions before execution |
| Plan/execute split with an explicit node-side step | `trace` → `host trace --plan` | Model for planned changes executed by a separately privileged component |
| Injectable environments (`HostEnv`, `ClientFactory`) | `internal/cli` | Agents can embed the engine without the CLI |

## Rules for current work

Until agent work starts, changes must keep the engine read-only: no write
verbs against the API, no state-changing commands in the host allowlist, no
packet modification in BPF programs.

`internal/archtest` enforces part of this today: engine packages may not import
any `remediation` package and may not call Create/Update/Patch/Apply/Delete
(SelfSubjectAccessReview, which persists nothing, is the only exception).
