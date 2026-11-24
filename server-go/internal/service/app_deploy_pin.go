package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// SetFeatureFlags tells the deploy service which dark features are on.
func (s *AppDeployService) SetFeatureFlags(flags features.Flags) { s.flags = flags }

func (s *AppDeployService) pinningOn(ctx context.Context) bool {
	return features.Enabled(ctx, s.flags, features.AppImagePin)
}

// pinForRollout makes a deploy run an image by digest and records how it is
// pulled (EXC-839). The digest is the one already on record (a rollback, or a
// deploy by digest), else what the registry names for the tag right now, asked
// once. An image that cannot be pinned fails the deploy with the registry's own
// refusal; it is never deployed as a tag instead.
//
// A node may reuse its cached copy only of a public image, one an anonymous
// manifest request can read. Any image that needs the project's credentials
// (a private registry, the platform's own) stays pinned but is pulled
// Always, so the node asks its registry whether this pod may have it. A
// recorded mode is kept on a rollback. Operator registries are never dialled.
func (s *AppDeployService) pinForRollout(ctx context.Context, app *apphost.App, cfg apphost.DeployConfig, meta deployMeta) (apphost.DeployConfig, deployMeta, error) {
	if !s.pinningOn(ctx) {
		return cfg, meta, nil
	}
	if s.internal.Names(cfg.Image) {
		cfg.ImagePull = apphost.ImagePullCredentialed
		return cfg, meta, nil
	}
	if _, local, err := s.localRegistryDigest(cfg.Image); local {
		cfg.ImagePull = apphost.ImagePullCredentialed
		return cfg, meta, err
	}
	pinned, err := s.pinnedImage(ctx, app, &cfg, &meta)
	if err != nil {
		return cfg, meta, fmt.Errorf("pin %s to a digest: %w", cfg.Image, err)
	}
	cfg.Image = pinned
	if cfg.ImagePull == "" {
		if cfg.ImagePull, err = s.pullMode(ctx, app.ProjectID, pinned); err != nil {
			return cfg, meta, fmt.Errorf("check whether %s is public: %w", pinned, err)
		}
	}
	return cfg, meta, nil
}

// pinnedImage names the image by digest, resolving the tag only when no
// digest is on record. A tag resolved here also settles the pull mode.
func (s *AppDeployService) pinnedImage(ctx context.Context, app *apphost.App, cfg *apphost.DeployConfig, meta *deployMeta) (string, error) {
	if _, digest, pinned := strings.Cut(cfg.Image, "@"); pinned {
		meta.digest = digest
		return cfg.Image, nil
	}
	if meta.digest != "" {
		meta.imageRef = cfg.Image
		return apphost.PinImage(cfg.Image, meta.digest), nil
	}
	digest, mode, err := s.resolveWithMode(ctx, app.ProjectID, cfg.Image)
	if err != nil {
		return "", err
	}
	meta.imageRef, meta.digest, cfg.ImagePull = cfg.Image, digest, mode
	return apphost.PinImage(cfg.Image, digest), nil
}

// notAnonymous is what a registry answers when an image needs credentials
// (or hides that it exists without them).
func notAnonymous(err error) bool {
	return errors.Is(err, imagedigest.ErrDenied) || errors.Is(err, imagedigest.ErrNotFound)
}

// resolveWithMode asks anonymously first: an answer makes the image public.
// Only a registry that refuses is asked again, with the project's credential.
func (s *AppDeployService) resolveWithMode(ctx context.Context, projectID, image string) (string, string, error) {
	if s.images == nil {
		return "", "", errNoImageResolver
	}
	digest, err := s.images.Resolve(ctx, image, nil)
	if err == nil {
		return digest, apphost.ImagePullPublic, nil
	}
	if !notAnonymous(err) {
		return "", "", err
	}
	cred, credErr := s.registryCredential(projectID, image)
	if credErr != nil {
		return "", "", credErr
	}
	if cred == nil {
		return "", "", err
	}
	digest, err = s.images.Resolve(ctx, image, cred)
	if err != nil {
		return "", "", err
	}
	return digest, apphost.ImagePullCredentialed, nil
}

// pullMode probes an image already named by digest, anonymously.
func (s *AppDeployService) pullMode(ctx context.Context, projectID, pinned string) (string, error) {
	if s.images == nil {
		return "", errNoImageResolver
	}
	_, err := s.images.Resolve(ctx, pinned, nil)
	switch {
	case err == nil:
		return apphost.ImagePullPublic, nil
	case notAnonymous(err):
		return apphost.ImagePullCredentialed, nil
	default:
		return "", err
	}
}
