package engineproxy

import (
	"regexp"
	"strconv"
	"strings"
)

// usernsRange is the one user namespace an app or its disk tool may ask for
// (EXC-682): root of the container mapped to a fixed range of the platform
// user's own ids, for users and groups alike. It is Podman's spelling of
// --userns=auto with an explicit mapping; any other mode (host, keep-id, a
// plain auto, another container's) is refused.
var usernsRange = regexp.MustCompile(`^auto:uidmapping=0:([0-9]+):([0-9]+),gidmapping=0:([0-9]+):([0-9]+)$`)

// A range is one whole container user space, on the grid after the platform
// user's own 65536 ids, which every shared container maps: base 65537 + k*65536.
const (
	usernsSize      = 65536
	usernsFirstBase = usernsSize + 1
	// UsernsLabel is the range "base:size" an app's volume and containers carry.
	UsernsLabel = "excalibase.userns"
)

// UsernsModeOf is the mode a container must carry to use a volume labelled with the range.
func UsernsModeOf(label string) string {
	base, size, _ := strings.Cut(label, ":")
	return "auto:uidmapping=0:" + base + ":" + size + ",gidmapping=0:" + base + ":" + size
}

func checkUsernsMode(_ Policy, value any) error {
	mode, _ := value.(string)
	match := usernsRange.FindStringSubmatch(mode)
	if match == nil {
		return refuse("HostConfig.UsernsMode %q is not allowed; only auto:uidmapping=0:<base>:65536,gidmapping=0:<base>:65536", mode)
	}
	if match[1] != match[3] || match[2] != match[4] {
		return refuse("HostConfig.UsernsMode %q must map users and groups to the same range", mode)
	}
	base, errBase := strconv.ParseUint(match[1], 10, 32)
	size, errSize := strconv.ParseUint(match[2], 10, 32)
	onGrid := errBase == nil && base >= usernsFirstBase && (base-1)%usernsSize == 0
	if errSize != nil || size != usernsSize || !onGrid || base+size > 1<<32 {
		return refuse("HostConfig.UsernsMode %q is not one of the platform user's whole ranges (65536 ids from 65537 + n*65536)", mode)
	}
	return nil
}
