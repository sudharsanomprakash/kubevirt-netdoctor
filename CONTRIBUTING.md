# Contributing to kubevirt-netdoctor

Thanks for your interest in improving KubeVirt network diagnostics. This document
explains how to build, test and propose changes.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development setup

Requirements:

- Go (the version in `go.mod`; with the default `GOTOOLCHAIN=auto` the Go command
  downloads the pinned toolchain automatically)
- [golangci-lint](https://golangci-lint.run/) v2 for `make lint`

```sh
make build        # builds bin/kubectl-netdoctor
make test         # unit and golden tests
make race         # tests with the race detector
make lint         # golangci-lint
```

`make integration` runs the host collector against real iproute2 output in a
disposable network namespace (requires root and iproute2; it removes everything it
creates). No cluster is required for any test.

No cluster is required for the test suite. Discovery is tested against fake
Kubernetes clients loaded from the YAML fixtures in `testdata/fixtures`.

## Project layout

| Path | Purpose |
| --- | --- |
| `cmd/kubectl-netdoctor` | Plugin entrypoint |
| `internal/cli` | Cobra commands, flags, golden tests |
| `internal/kube` | Client construction and API error classification |
| `internal/kubevirt` | VM/VMI types and virt-launcher pod discovery |
| `internal/multus` | Multus annotations, NAD lookup, CNI config parsing |
| `internal/topology` | Observed topology model and per-network correlation |
| `internal/inspect` | Orchestrates read-only discovery into a topology |
| `internal/host` | Host snapshot model, iproute2/OVS parsers, opt-in collector |
| `internal/hostpath` | Correlates a host snapshot with the topology |
| `internal/route` | Destination and route analysis |
| `internal/expect` | User-declared expectations |
| `internal/graph` | Topology graph with provenance |
| `internal/diagnose` | Findings engine and rules |
| `internal/output` | Text and JSON rendering |
| `testdata/fixtures` | Synthetic cluster state used by tests |
| `testdata/hosts` | Synthetic baseline host snapshots (scenarios mutate them in tests) |
| `testdata/golden` | Expected CLI output |

## Writing diagnostic rules

Diagnostics are the core of this project, and their credibility depends on never
overstating what was observed. When adding or changing a rule:

1. **Claim only what the API state supports.** If a fact depends on data that was not
   read (permissions, missing annotation, no guest agent), emit an `Unavailable`
   finding instead of guessing.
2. **Pick the category deliberately.** `Confirmed` means the API state directly
   establishes the problem. `Suspected` means it is consistent with a problem but has
   benign explanations (timing, version differences).
3. **Never report connectivity as working.** netdoctor does not send traffic.
4. **Include evidence and a concrete next action** in every finding.
5. **Never invent expected values.** Compare against declared configuration (VMI spec,
   NAD) or the user's expectations file, and state the source in `Expected`.
6. **Host collector changes** must keep commands read-only, fixed and shell-free; any
   new command needs a documented reason and an allowlist test.
7. **Keep finding IDs stable.** They are part of the JSON contract. Document new IDs
   in `docs/findings.md`.
8. Add unit tests in `internal/diagnose` and, where useful, a fixture plus golden test.

## Golden files

CLI output is covered by golden tests. After an intended output change:

```sh
make golden
git diff testdata/golden   # review every change
```

## Fixtures and examples

Use synthetic data only: documentation IP ranges (192.0.2.0/24, 198.51.100.0/24,
203.0.113.0/24) or private ranges, locally administered MAC addresses (`02:...`),
and generic names. Never copy manifests, logs or hostnames from real environments.
A test enforces the IP rule for fixtures.

## Commits and pull requests

- Keep pull requests focused; explain the problem and how you tested the change.
- Sign off your commits to certify the
  [Developer Certificate of Origin](https://developercertificate.org/):
  `git commit -s`.
- CI runs formatting, `go vet`, tests with the race detector, golden checks,
  golangci-lint and cross-compilation. All must pass before merge.
- Update `CHANGELOG.md` under *Unreleased* for user-visible changes.

## Releases

Versions follow [Semantic Versioning](https://semver.org/). A maintainer pushes a
`vX.Y.Z` tag on `main`; the release workflow runs the tests, cross-compiles the
binaries and publishes a GitHub release with checksums. v0.x releases are marked as
pre-releases.
