#!/usr/bin/env bash
# Creates, starts, stops or deletes the netdoctor lab VM on Google Compute Engine.
# Run on a workstation where `gcloud auth login` and a default project are set.
#
#   hack/lab/create-vm.sh auto     # create the VM and run the whole lab unattended
#   hack/lab/create-vm.sh results  # progress, or the test summary when finished
#   hack/lab/create-vm.sh create   # n2-standard-8, nested virtualization, Ubuntu 24.04
#   hack/lab/create-vm.sh ssh      # open a shell on it
#   hack/lab/create-vm.sh stop     # stop billing for CPU/RAM (disk is still billed)
#   hack/lab/create-vm.sh start
#   hack/lab/create-vm.sh delete   # remove the VM and its disk
set -euo pipefail

NAME="${LAB_NAME:-netdoctor-lab}"
ZONE="${LAB_ZONE:-us-central1-a}"
MACHINE="${LAB_MACHINE:-n2-standard-8}"
DISK_GB="${LAB_DISK_GB:-100}"

REPO="${LAB_REPO:-https://github.com/sudharsanomprakash/kubevirt-netdoctor}"
BRANCH="${LAB_BRANCH:-main}"

# Startup script for "auto": on first boot, clone the repository and run the
# whole lab unattended; progress goes to /var/log/netdoctor-lab.log.
startup_script() {
	cat <<-EOF
		#!/bin/bash
		[ -e /var/lib/netdoctor-lab/started ] && exit 0
		mkdir -p /var/lib/netdoctor-lab && touch /var/lib/netdoctor-lab/started
		exec >>/var/log/netdoctor-lab.log 2>&1
		set -x
		apt-get update -q && apt-get install -y -q git
		git clone --depth 1 -b "$BRANCH" "$REPO" /opt/kubevirt-netdoctor
		cd /opt/kubevirt-netdoctor
		export HOME=/root
		hack/lab/run-all.sh
	EOF
}

case "${1:-}" in
auto)
	# One command: create the VM and run setup, workloads and tests on it
	# unattended. Check progress later with: $0 results
	gcloud services enable compute.googleapis.com
	script="$(mktemp)"
	startup_script >"$script"
	gcloud compute instances create "$NAME" \
		--zone="$ZONE" --machine-type="$MACHINE" \
		--enable-nested-virtualization \
		--image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
		--boot-disk-size="${DISK_GB}GB" --boot-disk-type=pd-balanced \
		--labels=purpose=netdoctor-lab \
		--metadata-from-file=startup-script="$script"
	rm -f "$script"
	echo
	echo "The lab is building itself (about 30-45 minutes)."
	echo "Check progress any time with:  $0 results"
	;;
results)
	# Show progress, or the test summary once the run has finished.
	gcloud compute ssh "$NAME" --zone="$ZONE" --command='
		if [ -e /var/lib/netdoctor-lab/finished ]; then
			sudo tail -n 5 /var/log/netdoctor-lab.log
			sudo cat /var/lib/netdoctor-lab/results/summary.txt 2>/dev/null || sudo grep -n "FAILED\|error\|Error" /var/log/netdoctor-lab.log | tail -n 20
		else
			echo "Still running. Last log lines:"; sudo tail -n 15 /var/log/netdoctor-lab.log
		fi'
	;;
create)
	gcloud services enable compute.googleapis.com
	gcloud compute instances create "$NAME" \
		--zone="$ZONE" --machine-type="$MACHINE" \
		--enable-nested-virtualization \
		--image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
		--boot-disk-size="${DISK_GB}GB" --boot-disk-type=pd-balanced \
		--labels=purpose=netdoctor-lab
	echo "Waiting for SSH..."
	for _ in $(seq 1 30); do
		if gcloud compute ssh "$NAME" --zone="$ZONE" --command='test -e /dev/kvm && echo kvm-ok' 2>/dev/null | grep -q kvm-ok; then
			echo "VM is up and /dev/kvm is present."
			exit 0
		fi
		sleep 10
	done
	echo "VM created but SSH or /dev/kvm check did not succeed yet; retry: $0 ssh" >&2
	exit 1
	;;
ssh) shift; gcloud compute ssh "$NAME" --zone="$ZONE" "$@" ;;
start) gcloud compute instances start "$NAME" --zone="$ZONE" ;;
stop) gcloud compute instances stop "$NAME" --zone="$ZONE" ;;
delete) gcloud compute instances delete "$NAME" --zone="$ZONE" ;;
copy)
	# Copy the repository to the VM (run from the repository root).
	tar --exclude=.git --exclude=bin --exclude=dist -czf /tmp/netdoctor-src.tgz .
	gcloud compute scp /tmp/netdoctor-src.tgz "$NAME":~/ --zone="$ZONE"
	gcloud compute ssh "$NAME" --zone="$ZONE" --command='rm -rf ~/kubevirt-netdoctor && mkdir ~/kubevirt-netdoctor && tar -C ~/kubevirt-netdoctor -xzf ~/netdoctor-src.tgz'
	;;
*)
	echo "usage: $0 auto|results|create|ssh|start|stop|delete|copy" >&2
	exit 2
	;;
esac
