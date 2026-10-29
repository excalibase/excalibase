package service

import (
	"fmt"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

// SetLocalRegistries names the registries a self-hosted operator runs beside
// the platform (APP_LOCAL_REGISTRIES). The platform never dials them: a
// digest deploy runs as given and the container runtime pulls it.
func (s *AppDeployService) SetLocalRegistries(hosts []string) {
	s.localRegistries = slices.Clone(hosts)
}

// SetInternalRegistry names the platform's registry for git builds (EXC-540).
func (s *AppDeployService) SetInternalRegistry(internal *appregistry.Internal) {
	s.internal = internal
}

// internalDigest admits an image on the platform's registry only from the
// platform's own build, by digest, in the app's own repository; the registry
// is never dialled.
func (s *AppDeployService) internalDigest(app *apphost.App, image string, origin apphost.DeployOrigin) (string, string, error) {
	if origin.Source != apphost.DeploySourceGitBuild {
		return "", "", appregistry.ErrInternalImage
	}
	_, digest, _ := strings.Cut(image, "@")
	want, err := s.internal.Image(app.ProjectID, app.Name, digest)
	if err != nil || !strings.EqualFold(want, image) {
		return "", "", fmt.Errorf("%w: a build deploys %s's own repository by digest", appregistry.ErrInternalImage, app.Name)
	}
	return want, digest, nil
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
