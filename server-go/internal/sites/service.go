package sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

const (
	// MaxSitesPerProject bounds how many user Workers one project holds in the shared namespace.
	MaxSitesPerProject = 5
	// MaxDeployDuration is the lease one deploy or deletion holds on its site.
	MaxDeployDuration = 15 * time.Minute
	deployListLimit   = 50
)

// Publisher is the Workers for Platforms API a site is served through.
type Publisher interface {
	StartUploadSession(ctx context.Context, script string, manifest map[string]cfworkers.ManifestEntry) (cfworkers.UploadSession, error)
	UploadBucket(ctx context.Context, uploadJWT string, assets []cfworkers.Asset) (string, error)
	PutScript(ctx context.Context, script, completionJWT string, config cfworkers.AssetConfig) error
	DeleteScript(ctx context.Context, script string) error
}

// CorsOrigins is the project's CORS list, where an owner key marks the origins a site added.
type CorsOrigins interface {
	AddCorsOrigin(ctx context.Context, projectID, origin, owner string) (bool, []string, error)
	ReleaseAppCorsOrigins(ctx context.Context, projectID, owner string) ([]string, error)
}

// Options are the install's settings for Sites.
type Options struct {
	// BaseDomain is the user-content domain every site's hostname sits under.
	BaseDomain string
	// SpoolDir holds an upload while it is checked and sent; "" is the system temp dir.
	SpoolDir string
	// DeployLimits answers a project's limits; nil holds every project to DefaultLimits.
	DeployLimits DeployLimiter
}

// DeployLimiter answers what one project's deploys may hold, so a plan can raise Cloudflare's free-plan defaults.
type DeployLimiter interface {
	DeployLimits(ctx context.Context, projectID string) (Limits, error)
}

// tarFramingBytes covers tar headers and padding on top of the files themselves.
const tarFramingBytes = 1024

func (s *Service) limitsFor(ctx context.Context, projectID string) (Limits, error) {
	if s.options.DeployLimits == nil {
		return DefaultLimits, nil
	}
	limits, err := s.options.DeployLimits.DeployLimits(ctx, projectID)
	if err != nil {
		return Limits{}, fmt.Errorf("read the project's deploy limits: %w", err)
	}
	return limits, nil
}

// MaxUploadBytes bounds a deploy's request body before the archive is read.
func (s *Service) MaxUploadBytes(ctx context.Context, projectID string) (int64, error) {
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return 0, err
	}
	return limits.MaxTotalBytes + int64(2*limits.MaxFiles+2)*tarFramingBytes, nil
}

// PublishError is Cloudflare refusing or failing a deploy step.
type PublishError struct{ Err error }

func (e *PublishError) Error() string { return "the CDN did not take the deploy: " + e.Err.Error() }
func (e *PublishError) Unwrap() error { return e.Err }

// Service runs the Sites API. With no Publisher, Sites is not available on this install.
type Service struct {
	store     Store
	publisher Publisher
	origins   CorsOrigins
	options   Options
	now       func() time.Time
}

func NewService(store Store, publisher Publisher, origins CorsOrigins, options Options) (*Service, error) {
	if store == nil || origins == nil {
		return nil, errors.New("sites need a store and the project CORS list")
	}
	if publisher != nil && strings.TrimSpace(options.BaseDomain) == "" {
		return nil, errors.New("sites need a user-content base domain")
	}
	return &Service{store: store, publisher: publisher, origins: origins, options: options, now: time.Now}, nil
}

// Available reports whether this install has a Cloudflare account to publish sites with.
func (s *Service) Available() bool { return s != nil && s.publisher != nil }

func (s *Service) Create(ctx context.Context, projectID, name string, spaFallback *bool, actor string) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if err := ValidateName(name); err != nil {
		return nil, &InputError{Reason: err.Error()}
	}
	now := s.now().UTC()
	site := &Site{
		ID: uuid.NewString(), ProjectID: projectID, Name: name,
		Hostname:    name + "." + s.options.BaseDomain,
		SPAFallback: spaFallback == nil || *spaFallback,
		CreatedBy:   actor, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Create(ctx, site, MaxSitesPerProject); err != nil {
		return nil, err
	}
	return site, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	return s.store.List(ctx, projectID)
}

func (s *Service) Get(ctx context.Context, projectID, id string) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	return s.mustGet(ctx, projectID, id)
}

// UpdateSettings changes the routing the next deploy is published with.
func (s *Service) UpdateSettings(ctx context.Context, projectID, id string, spaFallback bool) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	return s.store.UpdateSettings(ctx, projectID, id, spaFallback)
}

func (s *Service) ListDeploys(ctx context.Context, projectID, siteID string) ([]*Deploy, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if _, err := s.mustGet(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	return s.store.ListDeploys(ctx, projectID, siteID, deployListLimit)
}

func (s *Service) GetDeploy(ctx context.Context, projectID, siteID, id string) (*Deploy, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	deploy, err := s.store.GetDeploy(ctx, projectID, siteID, id)
	if err != nil {
		return nil, err
	}
	if deploy == nil {
		return nil, ErrDeployNotFound
	}
	return deploy, nil
}

// Delete removes the Worker first, so a failure leaves the row to retry from.
func (s *Service) Delete(ctx context.Context, projectID, id string) error {
	if !s.Available() {
		return ErrSitesUnavailable
	}
	site, err := s.mustGet(ctx, projectID, id)
	if err != nil {
		return err
	}
	release, err := s.claim(ctx, site)
	if err != nil {
		return err
	}
	defer release()
	return s.remove(ctx, site)
}

func (s *Service) remove(ctx context.Context, site *Site) error {
	if err := s.publisher.DeleteScript(ctx, ScriptName(site)); err != nil {
		return &PublishError{Err: err}
	}
	if _, err := s.origins.ReleaseAppCorsOrigins(ctx, site.ProjectID, CorsOwner(site)); err != nil {
		return fmt.Errorf("release the site's CORS origin: %w", err)
	}
	return s.store.Delete(ctx, site.ProjectID, site.ID)
}

// PurgeProjectSites removes every site of a project being deleted.
func (s *Service) PurgeProjectSites(ctx context.Context, projectID string) (int, error) {
	held, err := s.store.List(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if len(held) > 0 && !s.Available() {
		return 0, fmt.Errorf("%w: the project still has %d sites to remove", ErrSitesUnavailable, len(held))
	}
	for _, site := range held {
		if err := s.Delete(ctx, projectID, site.ID); err != nil {
			return 0, fmt.Errorf("remove site %s: %w", site.Name, err)
		}
	}
	return len(held), nil
}

func (s *Service) mustGet(ctx context.Context, projectID, id string) (*Site, error) {
	site, err := s.store.Get(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if site == nil {
		return nil, ErrSiteNotFound
	}
	return site, nil
}

// claim takes the site's lease; release survives a caller that went away.
func (s *Service) claim(ctx context.Context, site *Site) (func(), error) {
	if err := s.store.ClaimDeploy(ctx, site.ProjectID, site.ID, s.now().Add(MaxDeployDuration)); err != nil {
		return nil, err
	}
	return func() {
		if err := s.store.ReleaseDeploy(context.WithoutCancel(ctx), site.ProjectID, site.ID); err != nil {
			log.Printf("site %s: release deploy lease: %v", site.ID, err)
		}
	}, nil
}
