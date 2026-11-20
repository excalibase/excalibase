package engineproxy

import (
	"regexp"
	"strconv"
)

// usernsRange is the one user namespace an app or its disk tool may ask for
// (EXC-682): root of the container mapped to a fixed range of the platform
// user's own ids, for users and groups alike. It is Podman's spelling of
// --userns=auto with an explicit mapping; any other mode (host, keep-id, a
// plain auto, another container's) is refused.
var usernsRange = regexp.MustCompile(`^auto:uidmapping=0:([0-9]+):([0-9]+),gidmapping=0:([0-9]+):([0-9]+)$`)

// maxUsernsSize is one container's whole user space: ids 0 to 65535.
const maxUsernsSize = 65536

func checkUsernsMode(_ Policy, value any) error {
	mode, _ := value.(string)
	match := usernsRange.FindStringSubmatch(mode)
	if match == nil {
		return refuse("HostConfig.UsernsMode %q is not allowed; only auto:uidmapping=0:<base>:<size>,gidmapping=0:<base>:<size>", mode)
	}
	if match[1] != match[3] || match[2] != match[4] {
		return refuse("HostConfig.UsernsMode %q must map users and groups to the same range", mode)
	}
	base, errBase := strconv.ParseUint(match[1], 10, 32)
	size, errSize := strconv.ParseUint(match[2], 10, 32)
	if errBase != nil || errSize != nil || base == 0 || size == 0 || size > maxUsernsSize || base+size > 1<<32 {
		return refuse("HostConfig.UsernsMode %q is not a range of the platform user's ids", mode)
	}
	return nil
}
