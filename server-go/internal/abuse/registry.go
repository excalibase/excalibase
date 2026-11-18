package abuse

import (
	"errors"
	"strings"
)

// AuditActionRegistryRefused is the audit action written when an app image is refused for its registry.
const AuditActionRegistryRefused = "app.refused.registry"

// blockedRegistries are registry hosts whose images are refused outright. To block another, add one line.
var blockedRegistries = []string{
	"ttl.sh", // anonymous throwaway registry, used for proxy nodes
}

// ErrBlockedRegistry is what a refused image's error matches; handlers answer it 403.
var ErrBlockedRegistry = errors.New("images from ttl.sh are not allowed; push to a registry such as Docker Hub or GHCR")

// RegistryRefusal is an image refused for the registry it lives in.
type RegistryRefusal struct{ Host string }

// Error stays generic: it names the registry but not why it is blocked.
func (e *RegistryRefusal) Error() string { return ErrBlockedRegistry.Error() }

func (e *RegistryRefusal) Unwrap() error { return ErrBlockedRegistry }

// imageRegistryHost is the registry host of an image reference, lower-cased and without port,
// or empty when the reference names no registry (a Docker Hub short name such as nginx or acme/app).
func imageRegistryHost(image string) string {
	first, _, hasPath := strings.Cut(strings.ToLower(strings.TrimSpace(image)), "/")
	if !hasPath {
		return ""
	}
	// Docker's rule: the first segment is a registry only if it has a dot or a port, or is localhost.
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return ""
	}
	host, _, _ := strings.Cut(first, ":")
	return strings.TrimSuffix(host, ".")
}

// BlockedRegistry reports whether the image is hosted by a blocked registry.
func BlockedRegistry(image string) bool {
	host := imageRegistryHost(image)
	for _, blocked := range blockedRegistries {
		if host == blocked {
			return true
		}
	}
	return false
}
