// Package k3smirror points the containerd inside a k3s test container at the Docker Hub mirror
// (EXC-881). Testcontainers starts the k3s image from the mirror, but the cluster then pulls its
// own images (CoreDNS, the pause image, whatever a test deploys) from docker.io; CI is rate limited
// there, so a registries.yaml sends those pulls to the mirror first.
package k3smirror

import (
	"fmt"
	"os"
	"strings"

	"github.com/testcontainers/testcontainers-go"
)

const (
	prefixEnv = "TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX"
	userEnv   = "MIRROR_REGISTRY_USER"
	tokenEnv  = "MIRROR_REGISTRY_TOKEN"

	registriesPath = "/etc/rancher/k3s/registries.yaml"
)

// Option writes registries.yaml into the k3s container when a mirror prefix is set, and does
// nothing otherwise, so a developer's run is unchanged.
func Option() testcontainers.ContainerCustomizer {
	content := registriesYAML(os.Getenv(prefixEnv), os.Getenv(userEnv), os.Getenv(tokenEnv))
	if content == "" {
		return testcontainers.CustomizeRequestOption(func(*testcontainers.GenericContainerRequest) error { return nil })
	}
	return testcontainers.WithFiles(testcontainers.ContainerFile{
		Reader:            strings.NewReader(content),
		ContainerFilePath: registriesPath,
		FileMode:          0o644,
	})
}

// registriesYAML is k3s's registry configuration for a prefix such as ghcr.io/excalibase/mirror/.
// The mirror keeps Docker Hub's paths below that prefix and drops library/, so the path is
// rewritten; a pull it cannot answer falls back to Docker Hub. Empty when the prefix names no
// registry host.
func registriesYAML(prefix, user, token string) string {
	host, path, found := strings.Cut(strings.Trim(prefix, "/"), "/")
	if !found || !strings.ContainsAny(host, ".:") {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "mirrors:\n  docker.io:\n    endpoint:\n      - https://%s\n    rewrite:\n      \"^(?:library/)?(.*)$\": \"%s/$1\"\n", host, path)
	if user != "" && token != "" {
		fmt.Fprintf(&out, "configs:\n  %s:\n    auth:\n      username: %s\n      password: %s\n", host, user, token)
	}
	return out.String()
}
