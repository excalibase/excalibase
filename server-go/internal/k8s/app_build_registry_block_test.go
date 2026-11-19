package k8s

import (
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/registryblock"
)

// The build NetworkPolicy works on addresses and cannot name a host, so each blocked registry
// is also pinned to loopback in the build pod's own hosts file (EXC-777).
func TestAppBuildPod_PinsEveryBlockedRegistryToLoopback(t *testing.T) {
	for _, builder := range []appbuilds.Builder{appbuilds.BuilderDockerfile, appbuilds.BuilderBuildpacks} {
		pod := renderAppBuildPod(testAppBuildOptions(), testAppRunSpec(builder))
		pinned := map[string][]string{}
		for _, alias := range pod.HostAliases {
			for _, host := range alias.Hostnames {
				pinned[host] = append(pinned[host], alias.IP)
			}
		}
		for _, host := range registryblock.Hosts() {
			ips := pinned[host]
			if !slices.Contains(ips, "127.0.0.1") || !slices.Contains(ips, "::1") {
				t.Errorf("%s: %s is pinned to %v, want 127.0.0.1 and ::1", builder, host, ips)
			}
		}
	}
}

// ExtraBlockedHosts is how the live build test pins a name that never resolves, to prove the pod's hosts file is honoured.
func TestAppBuildPod_PinsTheExtraBlockedHostsToo(t *testing.T) {
	options := testAppBuildOptions()
	options.ExtraBlockedHosts = []string{"blocked.invalid"}

	pod := renderAppBuildPod(options, testAppRunSpec(appbuilds.BuilderDockerfile))

	for _, alias := range pod.HostAliases {
		if !slices.Contains(alias.Hostnames, "blocked.invalid") || !slices.Contains(alias.Hostnames, "ttl.sh") {
			t.Errorf("alias %s pins %v, want the shared list and the extra host", alias.IP, alias.Hostnames)
		}
	}
	if len(pod.HostAliases) != 2 {
		t.Errorf("aliases = %v", pod.HostAliases)
	}
}
