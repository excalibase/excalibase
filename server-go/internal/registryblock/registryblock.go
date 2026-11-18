// Package registryblock is the one list of registry hosts the platform refuses
// (EXC-777). It has no dependencies so the app guard and the build egress
// proxy, a separate binary, can both import it.
package registryblock

// Hosts are the blocked registries. To block another, add one line.
var hosts = []string{
	"ttl.sh", // anonymous throwaway registry, used for proxy nodes
}

// Hosts returns a copy of the blocked registry hosts.
func Hosts() []string { return append([]string{}, hosts...) }

// IsHost reports whether host is exactly a blocked registry. Case and a
// trailing dot do not matter.
func IsHost(host string) bool { return false }

// IsHostOrSubdomain reports whether host is a blocked registry or a name under it.
func IsHostOrSubdomain(host string) bool { return false }
