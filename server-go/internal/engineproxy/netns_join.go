package engineproxy

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// A DocumentDB gateway serves from its database container's network namespace
// (EXC-576), as a sidecar does in a pod: it reaches Postgres on loopback, where
// pg_hba trusts it. Joining a namespace is otherwise how a container reaches
// another's loopback, so it is admitted only for an image the operator listed,
// only into a container provisioning manages (the handler checks that), and
// with no ports or networks of the joiner's own.

const netnsJoinPrefix = "container:"

var netnsJoinRef = regexp.MustCompile(`^` + ref + `$`)

// netnsJoin returns the container a NetworkMode joins, if it joins one.
func netnsJoin(mode any) (string, bool) {
	value, _ := mode.(string)
	return strings.CutPrefix(value, netnsJoinPrefix)
}

// NetnsJoinTarget is the container a create body joins the network namespace
// of, or "" when it joins none. Call it only on a body CheckCreate admitted.
func NetnsJoinTarget(body []byte) string {
	var create struct{ HostConfig map[string]any }
	if json.Unmarshal(body, &create) != nil {
		return ""
	}
	target, joins := netnsJoin(create.HostConfig["NetworkMode"])
	if !joins {
		return ""
	}
	return target
}

// checkNetnsJoin admits a create that joins another container's network
// namespace, or does nothing for one that does not. The NetworkMode field
// itself is then left to checkNetworkMode, which admits only what passed here.
func (p Policy) checkNetnsJoin(image string, hostConfig map[string]any, endpoints map[string]json.RawMessage) error {
	target, joins := netnsJoin(hostConfig["NetworkMode"])
	if !joins || len(p.NetnsJoinImages) == 0 {
		return nil
	}
	if !slices.Contains(p.NetnsJoinImages, image) {
		return refuse("image %q may not join another container's network namespace", image)
	}
	if !netnsJoinRef.MatchString(target) {
		return refuse("network mode names container %q, which is not a container reference", target)
	}
	if !isZero(hostConfig["PortBindings"]) {
		return refuse("a container in another's network namespace may not publish ports")
	}
	if len(endpoints) != 0 {
		return refuse("a container in another's network namespace may not join a network")
	}
	return nil
}

// netnsJoinAdmitted reports a NetworkMode checkNetnsJoin has vetted.
func (p Policy) netnsJoinAdmitted(mode string) bool {
	_, joins := netnsJoin(mode)
	return joins && len(p.NetnsJoinImages) > 0
}
