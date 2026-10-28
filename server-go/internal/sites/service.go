package sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/google/uuid"
)

const (
	// MaxDeployDuration is the lease one deploy or deletion holds on its site.
	MaxDeployDuration = 15 * time.Minute
	// publishTimeout ends every call to Cloudflare well inside the lease, so a
	// slow deploy cannot publish after a deletion has taken the site over.
	publishTimeout  = MaxDeployDuration - 3*time.Minute
	deployListLimit = 50
)

// Publisher is the Workers for Platforms API a site is served through.
type Publisher interface {
	StartUploadSession(ctx context.Context, script string, manifest map[string]cfworkers.ManifestEntry) (cfworkers.UploadSession, error)
	UploadBucket(ctx context.Context, uploadJWT string, assets []cfworkers.Asset) (string, error)
	PutScript(ctx context.Context, script string, version cfworkers.ScriptVersion) error
	DeleteScript(ctx context.Context, script string) error
	// PutKV and DeleteKV keep the hosts table the dispatch Worker routes by: hostname → script.
	PutKV(ctx context.Context, key, value string) error
	DeleteKV(ctx context.Context, key string) error
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
	// MaxDeploysInFlight caps uploads at once across the process; 0 is DefaultDeploysInFlight.
	MaxDeploysInFlight int
	// Hostnames keeps custom hostnames; deleting a site or project removes them through it.
	Hostnames HostnameStore
	// CustomDomains is the Cloudflare for SaaS zone; nil means custom domains are not offered.
	CustomDomains *CustomDomainOptions
	// Projects is the project lifecycle every route write follows; nil serves every project and holds nothing.
	Projects ProjectGate
	// Flags decide per request whether server-rendered sites are offered (features.SitesServer).
	Flags features.Flags
	// Code keeps server deploys' modules; nil offers no server sites.
	Code CodeStore
}

// DeployLimiter answers what one project's deploys may hold, so a plan can raise Cloudflare's free-plan defaults.
type DeployLimiter interface {
	DeployLimits(ctx context.Context, projectID string) (Limits, error)
}

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

// Limits answers what the project's plan lets its sites hold.
func (s *Service) Limits(ctx context.Context, projectID string) (Limits, error) {
	if !s.Available() {
		return Limits{}, ErrSitesUnavailable
	}
	return s.limitsFor(ctx, projectID)
}

// MaxUploadBytes bounds a deploy's request body before the archive is read;
// the archive's own reader then holds each kind to its exact limits.
func (s *Service) MaxUploadBytes(ctx context.Context, projectID string) (int64, error) {
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if s.serverSitesOn(ctx) {
		return MaxServerArchiveBytes(limits), nil
	}
	return MaxArchiveBytes(limits), nil
}

// serverSitesOn is whether server-rendered sites are offered to this request.
func (s *Service) serverSitesOn(ctx context.Context) bool {
	return s.options.Code != nil && features.Enabled(ctx, s.options.Flags, features.SitesServer)
}

// requireKind refuses work on a server site while server sites are off.
func (s *Service) requireKind(ctx context.Context, kind string) error {
	if kind == KindServer && !s.serverSitesOn(ctx) {
		return ErrServerSitesOff
	}
	return nil
}

// Steps a PublishError names.
const (
	stepStartUpload = "start the upload"
	stepUploadFiles = "upload the files"
	stepPublish     = "publish the site"
	stepDelete      = "delete the site"
	stepRoute       = "route the hostname"
	stepUnroute     = "stop routing the hostname"
)

// PublishError is Cloudflare refusing or failing a deploy step. Its message
// is fixed per step: Cloudflare's own text and paths name our account and
// namespace, so they go to the log only.
type PublishError struct {
	Step string
	Err  error
}

func (e *PublishError) Error() string {
	var apiErr *cfworkers.APIError
	if errors.As(e.Err, &apiErr) {
		code := apiErr.Status
		if len(apiErr.Codes) > 0 {
			code = apiErr.Codes[0]
		}
		return fmt.Sprintf("the CDN refused to %s (code %d)", e.Step, code)
	}
	return "the CDN could not be reached to " + e.Step
}

func (e *PublishError) Unwrap() error { return e.Err }

// publishFailure logs the full cause and answers the message users may see.
func publishFailure(site *Site, step string, err error) error {
	log.Printf("site %s: %s: %v", site.ID, step, err)
	return &PublishError{Step: step, Err: err}
}

// Service runs the Sites API. With no Publisher, Sites is not available on this install.
type Service struct {
	store     Store
	publisher Publisher
	origins   CorsOrigins
	options   Options
	admission *admission
	now       func() time.Time
}

func NewService(store Store, publisher Publisher, origins CorsOrigins, options Options) (*Service, error) {
	if store == nil || origins == nil {
		return nil, errors.New("sites need a store and the project CORS list")
	}
	if publisher != nil && strings.TrimSpace(options.BaseDomain) == "" {
		return nil, errors.New("sites need a user-content base domain")
	}
	if custom := options.CustomDomains; custom != nil && (custom.Zone == nil || custom.Target == "" || custom.Resolver == nil || options.Hostnames == nil) {
		return nil, errors.New("custom domains need the zone, the CNAME target, a DNS resolver and a hostname store")
	}
	return &Service{store: store, publisher: publisher, origins: origins, options: options,
		admission: newAdmission(options.MaxDeploysInFlight), now: time.Now}, nil
}

// Available reports whether this install has a Cloudflare account to publish sites with.
func (s *Service) Available() bool { return s != nil && s.publisher != nil }

func (s *Service) Create(ctx context.Context, projectID, name string, spaFallback *bool, actor string) (*Site, error) {
	return s.CreateKind(ctx, projectID, name, KindStatic, spaFallback, actor)
}

// CreateKind creates a static or a server-rendered site; the kind never changes after.
func (s *Service) CreateKind(ctx context.Context, projectID, name, kind string, spaFallback *bool, actor string) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if kind != KindStatic && kind != KindServer {
		return nil, &InputError{Reason: fmt.Sprintf("a site is %q or %q", KindStatic, KindServer)}
	}
	if err := s.requireKind(ctx, kind); err != nil {
		return nil, err
	}
	if err := ValidateName(name); err != nil {
		return nil, &InputError{Reason: err.Error()}
	}
	if custom := s.options.CustomDomains; custom != nil && name+"."+s.options.BaseDomain == custom.Target {
		return nil, &InputError{Reason: fmt.Sprintf("%q is reserved; choose another site name", name)}
	}
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	site := &Site{
		ID: uuid.NewString(), ProjectID: projectID, Name: name, Kind: kind,
		Hostname:    name + "." + s.options.BaseDomain,
		SPAFallback: spaFallback == nil || *spaFallback,
		CreatedBy:   actor, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Create(ctx, site, limits.MaxSites, limits.MaxServerSites); err != nil {
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
	lease, err := s.claim(ctx, site)
	if err != nil {
		return err
	}
	defer lease.release()
	return s.remove(ctx, site, lease)
}

func (s *Service) remove(ctx context.Context, site *Site, lease *siteLease) error {
	// Custom hostnames go first: each leaves the router, then the zone.
	if err := s.removeSiteHostnames(ctx, site); err != nil {
		return err
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	// The router stops sending traffic first, then the Worker goes.
	if err := s.publisher.DeleteKV(cdnCtx, site.Hostname); err != nil {
		return publishFailure(site, stepUnroute, err)
	}
	if err := s.publisher.DeleteScript(cdnCtx, ScriptName(site)); err != nil {
		return publishFailure(site, stepDelete, err)
	}
	if _, err := s.origins.ReleaseAppCorsOrigins(ctx, site.ProjectID, CorsOwner(site)); err != nil {
		return fmt.Errorf("release the site's CORS origin: %w", err)
	}
	return s.store.Delete(ctx, site.ProjectID, site.ID, lease.token)
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

// siteLease is one held claim on a site; release survives a caller that went away.
type siteLease struct {
	token   string
	release func()
}

// claim takes the site's lease under a fresh token, so only this holder can write under it.
func (s *Service) claim(ctx context.Context, site *Site) (*siteLease, error) {
	token := uuid.NewString()
	if err := s.store.ClaimDeploy(ctx, site.ProjectID, site.ID, token, s.now().Add(MaxDeployDuration)); err != nil {
		return nil, err
	}
	return &siteLease{token: token, release: func() {
		if err := s.store.ReleaseDeploy(context.WithoutCancel(ctx), site.ProjectID, site.ID, token); err != nil {
			log.Printf("site %s: release deploy lease: %v", site.ID, err)
		}
	}}, nil
}
