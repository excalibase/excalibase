package service

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// ImageResolver answers the digest an image reference names right now,
// asking with the credential when one is given.
type ImageResolver interface {
	Resolve(ctx context.Context, image string, cred *apphost.RegistryCredential) (string, error)
}

var errNoImageResolver = errors.New("deploying a named image needs a registry resolver, and none is configured")

func (s *AppDeployService) SetImageResolver(images ImageResolver) {
	s.images = images
}

// DeployImage deploys the app at the digest image names right now (EXC-543):
// the registry is asked once, with the project's saved credential for it, and
// the deploy runs that digest however the tag moves afterwards. The app keeps
// the reference as named, so the next plain deploy and the image watcher
// follow it.
func (s *AppDeployService) DeployImage(ctx context.Context, projectID, appID, image string, origin apphost.DeployOrigin) (*apphost.Deploy, error) {
	if err := apphost.ValidateImageReference(image); err != nil {
		return nil, err
	}
	if _, err := s.lookupApp(projectID, appID); err != nil {
		return nil, err
	}
	digest, err := s.resolveImage(ctx, projectID, image)
	if err != nil {
		return nil, err
	}
	return s.deployDigest(ctx, projectID, appID, image, digest, origin, nil)
}

// DeployCurrent deploys the app's own image, pinned like DeployImage: a
// digest reference runs that digest, and a tag runs the digest it names now.
// A registry on a private address may not be dialled by the platform, so a
// tag there runs as the node pulls it, unpinned.
func (s *AppDeployService) DeployCurrent(ctx context.Context, projectID, appID string, origin apphost.DeployOrigin) (*apphost.Deploy, error) {
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	if _, digest, pinned := strings.Cut(app.Image, "@"); pinned {
		return s.deployDigest(ctx, projectID, appID, app.Image, digest, origin, nil)
	}
	if s.images == nil {
		return s.DeployAppAs(ctx, projectID, appID, origin)
	}
	digest, err := s.resolveImage(ctx, projectID, app.Image)
	if errors.Is(err, imagedigest.ErrNotPublic) {
		return s.DeployAppAs(ctx, projectID, appID, origin)
	}
	if err != nil {
		return nil, err
	}
	return s.deployDigest(ctx, projectID, appID, app.Image, digest, origin, nil)
}

// resolveImage asks the image's registry with the project's saved credential
// for it; a credential that cannot be read is never an anonymous request.
func (s *AppDeployService) resolveImage(ctx context.Context, projectID, image string) (string, error) {
	if s.images == nil {
		return "", errNoImageResolver
	}
	cred, err := s.registryCredential(projectID, image)
	if err != nil {
		return "", err
	}
	return s.images.Resolve(ctx, image, cred)
}

// deployDigest records image and the digest it resolved to on the app, and
// rolls that digest out. still, when set, is asked under the lease whether
// the app as it is now still wants this deploy.
func (s *AppDeployService) deployDigest(ctx context.Context, projectID, appID, image, digest string,
	origin apphost.DeployOrigin, still func(*apphost.App) error) (*apphost.Deploy, error) {
	return s.underLease(ctx, projectID, appID, func(app *apphost.App) (*apphost.Deploy, func(), error) {
		if still != nil {
			if err := still(app); err != nil {
				return nil, nil, err
			}
		}
		cfg := apphost.ConfigFromApp(app)
		cfg.Image = apphost.PinImage(image, digest)
		meta := deployMeta{origin: origin, imageRef: image, digest: digest}
		deploy, startWatch, err := s.rollout(context.WithoutCancel(ctx), app, cfg, meta)
		if err != nil {
			return deploy, startWatch, err
		}
		// Only a recorded deploy moves the app to the image, so a refused one changes nothing.
		s.recordDeployedImage(projectID, appID, image, digest)
		return deploy, startWatch, nil
	})
}

// recordDeployedImage runs under the app's lease, after the deploy is recorded.
func (s *AppDeployService) recordDeployedImage(projectID, appID, image, digest string) {
	app, err := s.lookupApp(projectID, appID)
	if err == nil {
		app.Image, app.ResolvedDigest = image, digest
		err = s.apps.Update(app, app.Version)
	}
	if err != nil {
		log.Printf("record image %s on app %s/%s: %v", image, projectID, appID, err)
	}
}
