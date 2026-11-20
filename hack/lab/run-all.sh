#!/usr/bin/env bash
# Runs setup, workloads and tests in order and reports the real outcome.
# Safe to re-run after a failure: each step is idempotent.
#
#   sudo nohup hack/lab/run-all.sh >/var/log/netdoctor-lab.log 2>&1 &
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
mkdir -p /var/lib/netdoctor-lab
rm -f /var/lib/netdoctor-lab/finished
status=0
step() {
	echo "=== netdoctor-lab: $1 started $(date -u +%FT%TZ)"
	if "$@" 2>&1; then echo "=== netdoctor-lab: $1 OK"; else status=$?; echo "=== netdoctor-lab: $1 FAILED (exit $status)"; fi
}
step hack/lab/setup.sh
[ "$status" -eq 0 ] && step hack/lab/workloads.sh
[ "$status" -eq 0 ] && OUT=/var/lib/netdoctor-lab/results step hack/lab/run-tests.sh
if [ "$status" -eq 0 ]; then echo "netdoctor-lab: FINISHED OK"; else echo "netdoctor-lab: FINISHED WITH FAILURE (exit $status)"; fi
echo "$status" >/var/lib/netdoctor-lab/finished
exit "$status"
