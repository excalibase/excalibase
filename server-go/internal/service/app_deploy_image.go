package service

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/apphost"
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
	if s.images == nil {
		return nil, errNoImageResolver
	}
	cred, err := s.registryCredential(projectID, image)
	if err != nil {
		return nil, err
	}
	digest, err := s.images.Resolve(ctx, image, cred)
	if err != nil {
		return nil, err
	}
	return s.underLease(ctx, projectID, appID, func(app *apphost.App) (*apphost.Deploy, func(), error) {
		app.Image, app.ResolvedDigest = image, digest
		if err := s.apps.Update(app, app.Version); err != nil {
			return nil, nil, err
		}
		cfg := apphost.ConfigFromApp(app)
		cfg.Image = apphost.PinImage(image, digest)
		meta := deployMeta{origin: origin, imageRef: image, digest: digest}
		return s.rollout(context.WithoutCancel(ctx), app, cfg, meta)
	})
}
