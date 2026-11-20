#!/usr/bin/env bash
# Creates, starts, stops or deletes the netdoctor lab VM on Google Compute Engine.
# Run on a workstation where `gcloud auth login` and a default project are set.
#
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

case "${1:-}" in
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
	echo "usage: $0 create|ssh|start|stop|delete|copy" >&2
	exit 2
	;;
esac
