package sites

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

const (
	// PendingHostnameLifetime is how long a hostname may go without serving
	// before it is dropped, so nobody holds a domain they do not point here.
	PendingHostnameLifetime = 7 * 24 * time.Hour
	// ActiveHostnameRecheckInterval spaces the checks of a serving hostname:
	// its proof, Cloudflare's state and its certificate.
	ActiveHostnameRecheckInterval = time.Hour
	// WithdrawnHostnameCooldown spaces a withdrawn hostname's next proof, so an
	// answer flipping every few seconds cannot make Cloudflare re-issue its certificate each time.
	WithdrawnHostnameCooldown = 15 * time.Minute
	// DefaultMaxZoneHostnames keeps the zone under the 100 custom hostnames
	// Cloudflare includes; every one after is billed.
	DefaultMaxZoneHostnames = 90
	// hostnameRecheckInterval spaces Cloudflare calls for one hostname.
	hostnameRecheckInterval = 10 * time.Second
	pendingSweepBatch       = 50
)

// Steps a custom hostname PublishError names.
const (
	stepCheckHostname  = "check the custom domain"
	stepRemoveHostname = "remove the custom domain"
)

// CustomDomainOptions is the Cloudflare for SaaS zone custom hostnames are added to.
type CustomDomainOptions struct {
	Zone HostnameAPI
	// Target is the hostname customers point their CNAME at: the zone's fallback origin.
	Target string
	// ReservedDomains are the platform's own domains, refused as custom hostnames.
	ReservedDomains []string
	// Resolver reads the owner's TXT proof.
	Resolver TXTResolver
	// MaxOnZone bounds the install's custom hostnames; 0 is DefaultMaxZoneHostnames.
	MaxOnZone int
}

// ProjectGate is the project lifecycle a custom hostname's routing follows.
type ProjectGate interface {
	// HoldProject takes the project's lifecycle lease, which a deletion holds
	// while it unroutes; held is false while another operation has it.
	HoldProject(ctx context.Context, projectID string) (release func(), held bool, err error)
	// ProjectServing is false for a project scheduled for or under deletion.
	ProjectServing(ctx context.Context, projectID string) (bool, error)
}

// AddHostnameRequest is a user adding their own domain to a site.
type AddHostnameRequest struct {
	ProjectID string
	SiteID    string
	Hostname  string
	// Validation is http (the default) or txt.
	Validation string
	Actor      string
}

// CustomDomainsAvailable reports whether this install can add custom hostnames.
func (s *Service) CustomDomainsAvailable() bool {
	return s.Available() && s.options.CustomDomains != nil
}

func (s *Service) maxOnZone() int {
	if limit := s.options.CustomDomains.MaxOnZone; limit > 0 {
		return limit
	}
	return DefaultMaxZoneHostnames
}

// AddHostname claims the hostname for the site and answers the DNS records to
// add. Nothing reaches Cloudflare until a check sees the claim's TXT proof.
func (s *Service) AddHostname(ctx context.Context, request AddHostnameRequest) (*CustomHostname, error) {
	if !s.CustomDomainsAvailable() {
		return nil, ErrCustomDomainsUnavailable
	}
	validation, err := validationMethod(request.Validation)
	if err != nil {
		return nil, err
	}
	host, err := NormalizeHostname(request.Hostname, s.options.BaseDomain, s.options.CustomDomains.ReservedDomains)
	if err != nil {
		return nil, &InputError{Reason: err.Error()}
	}
	site, err := s.mustGet(ctx, request.ProjectID, request.SiteID)
	if err != nil {
		return nil, err
	}
	// The plan's limit (EXC-620's tier config); the store counts under a lock.
	limits, err := s.limitsFor(ctx, site.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := s.zoneHasRoom(ctx); err != nil {
		return nil, err
	}
	lease, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	hostname := s.newHostname(site, host, validation, request.Actor)
	if err := s.options.Hostnames.CreateHostname(ctx, hostname, limits.MaxCustomDomains); err != nil {
		if errors.Is(err, ErrHostnameLimitReached) {
			return nil, fmt.Errorf("%w: at most %d per project on its plan", err, limits.MaxCustomDomains)
		}
		return nil, err
	}
	return hostname, nil
}

// zoneHasRoom refuses a claim early when the zone is already full; the proof is the binding check.
func (s *Service) zoneHasRoom(ctx context.Context) error {
	held, err := s.options.Hostnames.CountVerifiedHostnames(ctx)
	if err != nil {
		return err
	}
	if held >= s.maxOnZone() {
		return ErrCustomDomainsFull
	}
	return nil
}

func (s *Service) newHostname(site *Site, host, validation, actor string) *CustomHostname {
	now := s.now().UTC()
	hostname := &CustomHostname{ID: uuid.NewString(), SiteID: site.ID, ProjectID: site.ProjectID, Hostname: host,
		Validation: validation, Status: HostnamePending, VerificationToken: uuid.NewString(),
		CreatedBy: actor, CreatedAt: now, UpdatedAt: now, InactiveSince: &now, Errors: []string{}}
	hostname.Records = dnsInstructions(hostname, s.options.CustomDomains.Target, cfworkers.CustomHostname{})
	return hostname
}

func validationMethod(requested string) (string, error) {
	switch requested {
	case "", cfworkers.ValidationHTTP:
		return cfworkers.ValidationHTTP, nil
	case cfworkers.ValidationTXT:
		return cfworkers.ValidationTXT, nil
	}
	return "", &InputError{Reason: "validation is http or txt"}
}

func (s *Service) ListHostnames(ctx context.Context, projectID, siteID string) ([]*CustomHostname, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if _, err := s.mustGet(ctx, projectID, siteID); err != nil {
		return nil, err
	}
	if s.options.Hostnames == nil {
		return []*CustomHostname{}, nil
	}
	return s.options.Hostnames.ListHostnames(ctx, projectID, siteID)
}

// RemoveHostname stops routing the hostname, then deletes it and its certificate from the zone.
func (s *Service) RemoveHostname(ctx context.Context, projectID, siteID, id string) error {
	if !s.Available() {
		return ErrSitesUnavailable
	}
	site, _, err := s.lookupHostname(ctx, projectID, siteID, id)
	if err != nil {
		return err
	}
	lease, err := s.claim(ctx, site)
	if err != nil {
		return err
	}
	defer lease.release()
	site, hostname, err := s.lookupHostname(ctx, projectID, siteID, id)
	if err != nil {
		return err
	}
	return s.removeHostname(ctx, site, hostname)
}

// removeHostname touches the router and the zone only for a proven row: an
// unproven claim was never there, and the name there may be another project's.
func (s *Service) removeHostname(ctx context.Context, site *Site, hostname *CustomHostname) error {
	if hostname.VerifiedAt != nil {
		if err := s.takeOffTheZone(ctx, site, hostname); err != nil {
			return err
		}
	}
	return s.options.Hostnames.DeleteHostname(ctx, site.ProjectID, site.ID, hostname.ID)
}

// takeOffTheZone unroutes a proven hostname, then deletes it from the zone and releases its CORS origin.
func (s *Service) takeOffTheZone(ctx context.Context, site *Site, hostname *CustomHostname) error {
	if s.options.CustomDomains == nil {
		return fmt.Errorf("%w: %s cannot be removed from the CDN", ErrCustomDomainsUnavailable, hostname.Hostname)
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := s.publisher.DeleteKV(cdnCtx, hostname.Hostname); err != nil {
		return publishFailure(site, stepUnroute, err)
	}
	if err := s.deleteFromZone(cdnCtx, hostname); err != nil {
		return publishFailure(site, stepRemoveHostname, err)
	}
	if _, err := s.origins.ReleaseAppCorsOrigins(ctx, site.ProjectID, HostnameCorsOwner(hostname)); err != nil {
		return fmt.Errorf("release the custom domain's CORS origin: %w", err)
	}
	return nil
}

// deleteFromZone also finds a hostname whose create was never answered, so
// none is left behind. Only a proven row calls it, and only one row holds a
// proof per hostname, so the zone's copy under that name is this one's.
func (s *Service) deleteFromZone(ctx context.Context, hostname *CustomHostname) error {
	zone := s.options.CustomDomains.Zone
	cloudflareID := hostname.CloudflareID
	if cloudflareID == "" {
		existing, err := zone.FindCustomHostname(ctx, hostname.Hostname)
		if err != nil || existing == nil {
			return err
		}
		cloudflareID = existing.ID
	}
	return zone.DeleteCustomHostname(ctx, cloudflareID)
}

// removeSiteHostnames removes every custom hostname of a site being deleted.
func (s *Service) removeSiteHostnames(ctx context.Context, site *Site) error {
	if s.options.Hostnames == nil {
		return nil
	}
	held, err := s.options.Hostnames.ListHostnames(ctx, site.ProjectID, site.ID)
	if err != nil {
		return err
	}
	for _, hostname := range held {
		if err := s.removeHostname(ctx, site, hostname); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) lookupHostname(ctx context.Context, projectID, siteID, id string) (*Site, *CustomHostname, error) {
	site, err := s.mustGet(ctx, projectID, siteID)
	if err != nil {
		return nil, nil, err
	}
	if s.options.Hostnames == nil {
		return nil, nil, ErrHostnameNotFound
	}
	hostname, err := s.options.Hostnames.GetHostname(ctx, projectID, siteID, id)
	if err != nil {
		return nil, nil, err
	}
	if hostname == nil {
		return nil, nil, ErrHostnameNotFound
	}
	return site, hostname, nil
}
