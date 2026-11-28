// Package memlimit gives a container's Go runtime a soft memory limit that is a share of the
// container's own, so the collector works harder before the kernel kills the process.
package memlimit

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// CgroupRoot is where a container sees its own cgroup.
const CgroupRoot = "/sys/fs/cgroup"

// unlimitedFloor is the size above which a cgroup v1 limit means none: the kernel reports
// its largest page-aligned value.
const unlimitedFloor = int64(1) << 62

// ContainerLimit is the memory limit of the cgroup at root, read from cgroup v2 or v1. A
// process outside a cgroup namespace sits in a directory below root, which selfCgroup (the
// contents of /proc/self/cgroup) names. It reports false when there is no limit or none can be read.
func ContainerLimit(root, selfCgroup string) (int64, bool) {
	directories := []string{root}
	if below := cgroupV2Path(selfCgroup); below != "" {
		directories = append(directories, filepath.Join(root, below))
	}
	for _, directory := range directories {
		for _, file := range []string{"memory.max", filepath.Join("memory", "memory.limit_in_bytes")} {
			if limit, ok := readLimit(filepath.Join(directory, file)); ok {
				return limit, true
			}
		}
	}
	return 0, false
}

// cgroupV2Path is the cgroup v2 line of /proc/self/cgroup, cleaned and kept inside the root.
func cgroupV2Path(selfCgroup string) string {
	for _, line := range strings.Split(selfCgroup, "\n") {
		if path, found := strings.CutPrefix(line, "0::"); found {
			cleaned := filepath.Join("/", path)
			return strings.TrimPrefix(cleaned, "/")
		}
	}
	return ""
}

func readLimit(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || limit <= 0 || limit >= unlimitedFloor {
		return 0, false
	}
	return limit, true
}

// SoftLimit is percent of the container's limit, or false when the container has none, the
// share is not between 1 and 100, or goMemLimit (the GOMEMLIMIT setting) is already chosen.
func SoftLimit(root, selfCgroup string, percent int, goMemLimit string) (int64, bool) {
	if goMemLimit != "" || percent < 1 || percent > 100 {
		return 0, false
	}
	limit, ok := ContainerLimit(root, selfCgroup)
	if !ok {
		return 0, false
	}
	return limit / 100 * int64(percent), true
}

// Apply sets the runtime's soft limit to percent of the container's and returns it. goMemLimit
// is the GOMEMLIMIT setting as the caller read it; when set, Apply leaves the limit alone.
func Apply(percent int, goMemLimit string) (int64, bool) {
	selfCgroup, _ := os.ReadFile("/proc/self/cgroup")
	limit, ok := SoftLimit(CgroupRoot, string(selfCgroup), percent, goMemLimit)
	if ok {
		debug.SetMemoryLimit(limit)
	}
	return limit, ok
}
