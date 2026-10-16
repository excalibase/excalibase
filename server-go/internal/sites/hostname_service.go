package sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

const (
	// PendingHostnameLifetime is how long a hostname may go without serving
	// before it is dropped, so nobody holds a domain they do not point here.
	PendingHostnameLifetime = 7 * 24 * time.Hour
	// hostnameRecheckInterval spaces Cloudflare calls for one hostname.
	hostnameRecheckInterval = 10 * time.Second
	pendingSweepBatch       = 50
)

// Steps a custom hostname PublishError names.
const (
	stepAddHostname    = "add the custom domain"
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
	// Projects answers whether a project may serve; nil serves every project.
	Projects ProjectStatusReader
}

// ProjectStatusReader tells a project that may serve from one scheduled for deletion.
type ProjectStatusReader interface {
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

// AddHostname claims the hostname, then creates it on the zone. A failed
// create drops the claim only once nothing of it is left on the zone.
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
	answer, err := s.createOnZone(ctx, hostname)
	if err != nil {
		s.dropClaim(context.WithoutCancel(ctx), site, hostname, err)
		return nil, publishFailure(site, stepAddHostname, err)
	}
	return s.applyAnswer(context.WithoutCancel(ctx), site, hostname, answer)
}

func (s *Service) newHostname(site *Site, host, validation, actor string) *CustomHostname {
	now := s.now().UTC()
	hostname := &CustomHostname{ID: uuid.NewString(), SiteID: site.ID, ProjectID: site.ProjectID, Hostname: host,
		Validation: validation, Status: HostnamePending, VerificationToken: uuid.NewString(),
		CreatedBy: actor, CreatedAt: now, UpdatedAt: now, InactiveSince: &now, Errors: []string{}}
	hostname.Records = dnsInstructions(hostname, s.options.CustomDomains.Target, cfworkers.CustomHostname{})
	return hostname
}

// dropClaim forgets a hostname whose create failed. A refusal created nothing;
// any other failure may have, so the zone is cleaned first, and the claim is
// kept for the sweep to settle when that cannot be done either.
func (s *Service) dropClaim(ctx context.Context, site *Site, hostname *CustomHostname, createErr error) {
	if !definiteRefusal(createErr) {
		cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		if err := s.deleteFromZone(cdnCtx, hostname); err != nil {
			log.Printf("site %s: %s may be on the zone and could not be removed; kept for the next check: %v", site.ID, hostname.Hostname, err)
			return
		}
	}
	if err := s.options.Hostnames.DeleteHostname(ctx, site.ProjectID, site.ID, hostname.ID); err != nil {
		log.Printf("site %s: drop the claim on %s: %v", site.ID, hostname.Hostname, err)
	}
}

// definiteRefusal is Cloudflare answering that it did not create the hostname.
func definiteRefusal(err error) bool {
	var apiErr *cfworkers.APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && !cfworkers.IsDuplicateCustomHostname(err)
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

// createOnZone creates the hostname, adopting the zone's own copy when an
// earlier create got through but its answer was lost: the claim in the
// platform database already holds the name, and routing waits for the owner's proof.
func (s *Service) createOnZone(ctx context.Context, hostname *CustomHostname) (cfworkers.CustomHostname, error) {
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	zone := s.options.CustomDomains.Zone
	answer, err := zone.CreateCustomHostname(cdnCtx, hostname.Hostname, hostname.Validation)
	if !cfworkers.IsDuplicateCustomHostname(err) {
		return answer, err
	}
	existing, findErr := zone.FindCustomHostname(cdnCtx, hostname.Hostname)
	if findErr != nil {
		return cfworkers.CustomHostname{}, errors.Join(err, findErr)
	}
	if existing == nil {
		return cfworkers.CustomHostname{}, err
	}
	return *existing, nil
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

// RefreshHostname reads the hostname's state from Cloudflare, asks it to
// validate again while it is not active, and routes it once it is.
func (s *Service) RefreshHostname(ctx context.Context, projectID, siteID, id string) (*CustomHostname, error) {
	if !s.CustomDomainsAvailable() {
		return nil, ErrCustomDomainsUnavailable
	}
	site, hostname, err := s.lookupHostname(ctx, projectID, siteID, id)
	if err != nil {
		return nil, err
	}
	if s.now().Sub(hostname.CheckedAt) < hostnameRecheckInterval {
		return hostname, nil
	}
	lease, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	// What was read before the lease may have changed; only the held state is acted on.
	site, hostname, err = s.lookupHostname(ctx, projectID, siteID, id)
	if err != nil {
		return nil, err
	}
	return s.refreshHeld(ctx, site, hostname)
}

// refreshHeld runs a refresh under a lease the caller holds. A failed check
// is recorded too, so the sweep moves on and callers stay throttled.
func (s *Service) refreshHeld(ctx context.Context, site *Site, hostname *CustomHostname) (*CustomHostname, error) {
	answer, err := s.readZone(ctx, hostname)
	if err != nil {
		failed := *hostname
		failed.CheckedAt, failed.UpdatedAt = s.now().UTC(), s.now().UTC()
		failed.Errors = []string{"the CDN could not be asked about this domain; it is checked again in a few minutes"}
		if saveErr := s.options.Hostnames.UpdateHostname(context.WithoutCancel(ctx), &failed); saveErr != nil {
			log.Printf("site %s: record the failed check of %s: %v", site.ID, hostname.Hostname, saveErr)
		}
		return nil, publishFailure(site, stepCheckHostname, err)
	}
	return s.applyAnswer(context.WithoutCancel(ctx), site, hostname, answer)
}

// readZone answers the hostname's state; one not yet active is nudged to
// validate again, and one gone from the zone is created again.
func (s *Service) readZone(ctx context.Context, hostname *CustomHostname) (cfworkers.CustomHostname, error) {
	if hostname.CloudflareID == "" {
		return s.createOnZone(ctx, hostname)
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	zone := s.options.CustomDomains.Zone
	answer, err := zone.GetCustomHostname(cdnCtx, hostname.CloudflareID)
	var apiErr *cfworkers.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return s.createOnZone(ctx, hostname)
	}
	if err != nil || hostnameStatus(answer) == HostnameActive {
		return answer, err
	}
	return zone.RevalidateCustomHostname(cdnCtx, hostname.CloudflareID, hostname.Validation)
}

// applyAnswer records Cloudflare's answer and the owner's proof, and moves
// the router and the CORS list with them: an active hostname of a deployed
// site is routed, any other is not.
func (s *Service) applyAnswer(ctx context.Context, site *Site, hostname *CustomHostname, answer cfworkers.CustomHostname) (*CustomHostname, error) {
	previous := hostname.Status
	next := *hostname
	now := s.now().UTC()
	if answer.ID != "" {
		next.CloudflareID = answer.ID
	}
	next.HostnameStatus, next.CertificateStatus = answer.Status, answer.SSL.Status
	next.Errors = hostnameErrors(answer)
	s.checkOwnership(ctx, &next)
	next.Status = hostnameStatus(answer)
	if next.Status == HostnameActive && next.VerifiedAt == nil {
		next.Status = HostnamePending
	}
	next.Records = dnsInstructions(&next, s.options.CustomDomains.Target, answer)
	next.CheckedAt, next.UpdatedAt = now, now
	switch {
	case next.Status == HostnameActive:
		next.InactiveSince = nil
	case next.InactiveSince == nil:
		next.InactiveSince = &now
	}
	if err := s.routeHostname(ctx, site, &next, previous); err != nil {
		return nil, err
	}
	if err := s.options.Hostnames.UpdateHostname(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *Service) routeHostname(ctx context.Context, site *Site, hostname *CustomHostname, previous string) error {
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if hostname.Status != HostnameActive {
		if previous != HostnameActive {
			return nil
		}
		if err := s.publisher.DeleteKV(cdnCtx, hostname.Hostname); err != nil {
			return publishFailure(site, stepUnroute, err)
		}
		if _, err := s.origins.ReleaseAppCorsOrigins(ctx, site.ProjectID, HostnameCorsOwner(hostname)); err != nil {
			return fmt.Errorf("release the custom domain's CORS origin: %w", err)
		}
		return nil
	}
	serving, err := s.projectServing(ctx, site.ProjectID)
	if err != nil {
		return err
	}
	if !serving {
		// A cancelled deletion routes it again with the project's other sites.
		return nil
	}
	if site.LiveDeployID != "" {
		if err := s.publisher.PutKV(cdnCtx, hostname.Hostname, ScriptName(site)); err != nil {
			return publishFailure(site, stepRoute, err)
		}
	}
	if previous != HostnameActive {
		if _, _, err := s.origins.AddCorsOrigin(ctx, site.ProjectID, "https://"+hostname.Hostname, HostnameCorsOwner(hostname)); err != nil {
			return fmt.Errorf("add the custom domain's origin to the project's CORS list: %w", err)
		}
	}
	return nil
}

func (s *Service) projectServing(ctx context.Context, projectID string) (bool, error) {
	gate := s.options.CustomDomains.Projects
	if gate == nil {
		return true, nil
	}
	serving, err := gate.ProjectServing(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("read whether the project is being deleted: %w", err)
	}
	return serving, nil
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

func (s *Service) removeHostname(ctx context.Context, site *Site, hostname *CustomHostname) error {
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
	return s.options.Hostnames.DeleteHostname(ctx, site.ProjectID, site.ID, hostname.ID)
}

// deleteFromZone also finds a hostname whose create was never answered, so
// none is left behind. The claim in the platform database holds the name, so
// the zone's copy under it is this site's.
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

// routeActiveHostnames writes the site's active custom hostnames into the router.
func (s *Service) routeActiveHostnames(ctx context.Context, site *Site) error {
	if s.options.Hostnames == nil {
		return nil
	}
	held, err := s.options.Hostnames.ListHostnames(ctx, site.ProjectID, site.ID)
	if err != nil {
		return err
	}
	for _, hostname := range held {
		if hostname.Status != HostnameActive {
			continue
		}
		if err := s.publisher.PutKV(ctx, hostname.Hostname, ScriptName(site)); err != nil {
			return publishFailure(site, stepRoute, err)
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
