#!/usr/bin/env bash
# Writes the krew plugin manifest for a published release, using the
# release's checksums.txt (archive names come from `make dist`).
#
#   hack/krew/manifest.sh v0.1.0 > .krew/netdoctor.yaml
set -euo pipefail
VERSION="${1:?usage: $0 vX.Y.Z}"
REPO="sudharsanomprakash/kubevirt-netdoctor"
BASE="https://github.com/$REPO/releases/download/$VERSION"
SUMS="$(curl -fsSL "$BASE/checksums.txt")"

cat <<YAML
apiVersion: krew.googlecontainertools.github.com/v1alpha2
kind: Plugin
metadata:
  name: netdoctor
spec:
  version: $VERSION
  homepage: https://github.com/$REPO
  shortDescription: Diagnose KubeVirt virtual machine networking
  description: |
    netdoctor explains how a KubeVirt VM is wired into the network and what is
    wrong with it: VMI interfaces, the virt-launcher pod, Multus attachments,
    NetworkAttachmentDefinitions and, from an explicitly collected host
    snapshot, the node's bridges, OVS ports, VLANs, bonds and routes. Findings
    state what is wrong, how certain that is, what was expected and what to
    check next. Cluster commands are read-only and send no traffic.
  caveats: |
    Cluster commands need read access to VMs, VMIs, pods and
    NetworkAttachmentDefinitions. Host commands (host collect, host trace)
    run on a node and are started explicitly by an operator.
    Docs: https://github.com/$REPO#readme
  platforms:
YAML
for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
	os="${p%/*}" arch="${p#*/}"
	name="kubectl-netdoctor_${VERSION}_${os}_${arch}"
	sha="$(awk -v f="$name.tar.gz" '$2 == f {print $1}' <<<"$SUMS")"
	[ -n "$sha" ] || { echo "no checksum for $name.tar.gz" >&2; exit 1; }
	bin=kubectl-netdoctor
	[ "$os" = windows ] && bin=kubectl-netdoctor.exe
	cat <<YAML
  - selector:
      matchLabels:
        os: $os
        arch: $arch
    uri: $BASE/$name.tar.gz
    sha256: $sha
    files:
    - from: $name/$bin
      to: .
    - from: $name/LICENSE
      to: .
    bin: $bin
YAML
done
