package engineproxy

import "strconv"

// HostPortRange is the window of host ports a tenant container may publish
// on by number (EXC-592), so its clients keep one port across restarts. A
// zero range admits none: the engine picks every port.
type HostPortRange struct {
	Low, High int
}

// admits reports whether a requested host port is in the range.
func (r HostPortRange) admits(hostPort string) bool {
	port, err := strconv.Atoi(hostPort)
	if err != nil || strconv.Itoa(port) != hostPort {
		return false
	}
	return r.Low > 0 && port >= r.Low && port <= r.High
}
