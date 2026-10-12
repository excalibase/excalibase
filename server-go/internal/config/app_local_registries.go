package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// registryHost is a registry host with an optional port, as an image names it.
var registryHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*(?::([0-9]{1,5}))?$`)

// loadAppLocalRegistries reads APP_LOCAL_REGISTRIES: registries a self-hosted
// operator runs beside the platform, e.g. localhost:5000. A digest deploy
// from one runs as given; the platform never dials them (EXC-600).
func loadAppLocalRegistries() []string {
	var hosts []string
	for _, entry := range strings.Split(os.Getenv("APP_LOCAL_REGISTRIES"), ",") {
		if host := strings.ToLower(strings.TrimSpace(entry)); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// dockerHubHosts are Docker Hub's names: listing one would skip the digest
// check for every Hub image.
var dockerHubHosts = map[string]bool{"docker.io": true, "index.docker.io": true, "registry-1.docker.io": true}

func (c AppConfig) validateAppLocalRegistries() error {
	if len(c.AppLocalRegistries) == 0 {
		return nil
	}
	if c.IsCloud() {
		return fmt.Errorf("APP_LOCAL_REGISTRIES is for a self-hosted platform: in cloud mode it would let any tenant deploy from those addresses")
	}
	for _, host := range c.AppLocalRegistries {
		if dockerHubHosts[host] {
			return fmt.Errorf("APP_LOCAL_REGISTRIES: %q is Docker Hub, not a registry on this host", host)
		}
		match := registryHost.FindStringSubmatch(host)
		if match == nil {
			return fmt.Errorf("APP_LOCAL_REGISTRIES: %q is not a registry host (host or host:port)", host)
		}
		if port := match[1]; port != "" {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("APP_LOCAL_REGISTRIES: %q has no valid port", host)
			}
		}
	}
	return nil
}
