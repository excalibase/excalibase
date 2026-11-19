package abuse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/registryblock"
)

// AuditActionRegistryRefused is the audit action written when an app image is refused for its registry.
const AuditActionRegistryRefused = "app.refused.registry"

// ErrBlockedRegistry is what a refused image's error matches; handlers answer it 403.
// The hosts themselves live in registryblock, shared with the build egress proxy.
var ErrBlockedRegistry = errors.New("images from this registry are not allowed; push to a registry such as Docker Hub or GHCR")

// RegistryRefusal is an image refused for the registry it lives in.
type RegistryRefusal struct{ Host string }

// Error stays generic: it names the registry but not why it is blocked.
func (e *RegistryRefusal) Error() string {
	return fmt.Sprintf("images from %s are not allowed; push to a registry such as Docker Hub or GHCR", e.Host)
}

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

// BlockedRegistry reports whether the image is hosted by a blocked registry or a subdomain of one,
// the same rule the build egress proxy applies.
func BlockedRegistry(image string) bool {
	return registryblock.IsHostOrSubdomain(imageRegistryHost(image))
}
