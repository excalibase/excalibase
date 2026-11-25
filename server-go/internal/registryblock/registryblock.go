// Package registryblock is the one list of registry hosts the platform refuses
// (EXC-777). It has no dependencies so the app guard and the build egress
// proxy, a separate binary, can both import it.
package registryblock

import (
	"slices"
	"strings"
)

// Hosts are the blocked registries. To block another, add one line.
var hosts = []string{
	"ttl.sh", // anonymous throwaway registry, used for proxy nodes
}

// Hosts returns a copy of the blocked registry hosts.
func Hosts() []string { return append([]string{}, hosts...) }

// IsHost reports whether host is exactly a blocked registry. Case and a
// trailing dot do not matter.
func IsHost(host string) bool {
	return slices.Contains(hosts, normalise(host))
}

// IsHostOrSubdomain reports whether host is a blocked registry or a name under it.
func IsHostOrSubdomain(host string) bool {
	name := normalise(host)
	return slices.ContainsFunc(hosts, func(blocked string) bool {
		return name == blocked || strings.HasSuffix(name, "."+blocked)
	})
}

func normalise(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
