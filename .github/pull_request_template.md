## What this PR does

<!-- Describe the change and the problem it solves. Link related issues (Fixes #123). -->

## How it was tested

<!-- Unit tests, golden tests, manual runs against a cluster (describe the environment). -->

## Checklist

- [ ] `make fmt vet test race lint` passes locally
- [ ] Golden files regenerated with `make golden` if output changed, and the diff reviewed
- [ ] New diagnostics only claim what the observed API state supports (see CONTRIBUTING.md)
- [ ] Required RBAC permissions documented if new resources are read
- [ ] Fixtures and examples use synthetic data only
- [ ] Commits are signed off (`git commit -s`)
