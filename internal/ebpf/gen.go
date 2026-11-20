// Package ebpf loads netdoctor's kernel tracer and turns its output into
// normalized trace events. The kernel programs live in bpf/netdoctor.bpf.c.
package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -tags linux -target amd64,arm64 -type event -type ifkey -type addrkey -type ifstatkey -type ifstat netdoctor ../../bpf/netdoctor.bpf.c -- -I../../bpf -O2 -g -Wall -Werror
