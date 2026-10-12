package service

import (
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// SetLocalRegistries names the registries a self-hosted operator runs beside
// the platform (APP_LOCAL_REGISTRIES). The platform never dials them: a
// digest deploy runs as given and the container runtime pulls it.
func (s *AppDeployService) SetLocalRegistries(hosts []string) {
	s.localRegistries = slices.Clone(hosts)
}

// localRegistryDigest answers whether image is on an operator registry, and
// the digest it pins when it is.
func (s *AppDeployService) localRegistryDigest(image string) (string, bool, error) {
	if len(s.localRegistries) == 0 {
		return "", false, nil
	}
	host, err := apphost.NormalizeRegistry(apphost.ImageRegistry(image))
	if err != nil || !slices.Contains(s.localRegistries, host) {
		return "", false, nil
	}
	_, digest, pinned := strings.Cut(image, "@")
	if !pinned {
		return "", true, apphost.ErrLocalRegistryNeedsDigest
	}
	return digest, true, nil
}
