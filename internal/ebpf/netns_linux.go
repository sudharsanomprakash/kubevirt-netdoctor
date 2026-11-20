//go:build linux

package ebpf

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// NetnsID returns the inode number identifying the network namespace at
// path (for example /proc/self/ns/net or /proc/<pid>/ns/net). It is the
// same value the kernel programs read from net->ns.inum.
func NetnsID(path string) (uint32, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("cannot stat %s", path)
	}
	return uint32(st.Ino), nil //nolint:gosec // ns.inum is an unsigned int in the kernel
}

// ResolveInterface resolves an interface name in the network namespace at
// nsPath ("" for the caller's own namespace) into a filter entry.
func ResolveInterface(nsPath, name string) (InterfaceFilter, error) {
	if nsPath == "" {
		nsPath = "/proc/self/ns/net"
	}
	id, err := NetnsID(nsPath)
	if err != nil {
		return InterfaceFilter{}, fmt.Errorf("network namespace %s: %w", nsPath, err)
	}
	var ifi *net.Interface
	err = inNetns(nsPath, func() error {
		var e error
		ifi, e = net.InterfaceByName(name)
		return e
	})
	if err != nil {
		return InterfaceFilter{}, fmt.Errorf("interface %s in %s: %w", name, nsPath, err)
	}
	return InterfaceFilter{Name: name, Index: uint32(ifi.Index), NetnsID: id}, nil //nolint:gosec // ifindex is a positive int in the kernel
}

// inNetns runs fn with the calling OS thread switched into the namespace at
// nsPath. The thread is locked for the duration; if switching back fails,
// the thread is left locked so the Go runtime discards it.
func inNetns(nsPath string, fn func() error) error {
	self, err := NetnsID("/proc/thread-self/ns/net")
	if err != nil {
		return err
	}
	target, err := NetnsID(nsPath)
	if err != nil {
		return err
	}
	if self == target {
		return fn()
	}
	runtime.LockOSThread()
	orig, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer orig.Close()
	ns, err := os.Open(nsPath)
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer ns.Close()
	if err := unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("entering network namespace: %w", err)
	}
	ferr := fn()
	if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("returning to the original network namespace: %w", err)
	}
	runtime.UnlockOSThread()
	return ferr
}
