package ebpf

import "strings"

// cgroupHasPod reports whether a /proc/<pid>/cgroup file belongs to the pod.
// Kubelet writes the pod UID into the cgroup path with dashes (cgroupfs
// driver, ".../pod<uid>/...") or underscores (systemd driver,
// "...-pod<uid_with_underscores>.slice/...").
func cgroupHasPod(cgroup, podUID string) bool {
	if podUID == "" {
		return false
	}
	u := strings.ToLower(podUID)
	return strings.Contains(cgroup, "pod"+u) || strings.Contains(cgroup, "pod"+strings.ReplaceAll(u, "-", "_"))
}
