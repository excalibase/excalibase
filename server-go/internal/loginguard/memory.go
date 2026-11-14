package loginguard

import (
	"os"
	"strconv"
	"strings"
)

// cgroup files that hold a container's memory limit, v2 then v1.
var memoryLimitFiles = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}

// notALimit is where cgroup v1 reports "unlimited": a number near 2^63.
const notALimit = uint64(1) << 60

// ContainerMemoryLimit is the container's memory limit in bytes, or 0 when
// there is none or it cannot be read.
func ContainerMemoryLimit() uint64 {
	return readMemoryLimit(memoryLimitFiles)
}

func readMemoryLimit(paths []string) uint64 {
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		limit, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || limit == 0 || limit >= notALimit {
			continue
		}
		return limit
	}
	return 0
}
