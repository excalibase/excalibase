package sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// routing is what a check may assume when it routes an active hostname.
type routing struct {
	// serving answers whether the project may serve; a cancelled deletion routes before its status is written.
	serving func(ctx context.Context, projectID string) (bool, error)
	// live is whether the site's Worker exists to route to.
	live bool
}

func (s *Service) routingFor(site *Site) routing {
	return routing{serving: s.projectServing, live: site.LiveDeployID != ""}
}

func assumeServing(context.Context, string) (bool, error) { return true, nil }

// RefreshHostname checks the owner's proof and Cloudflare's state, under the
// project's lifecycle lease so a deletion's unroute cannot interleave.
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
	release, err := s.holdProject(ctx, site.ProjectID)
	if err != nil {
		return nil, err
	}
	defer release()
	lease, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	// What was read before the leases may have changed; only the held state is acted on.
	site, hostname, err = s.lookupHostname(ctx, projectID, siteID, id)
	if err != nil {
		return nil, err
	}
	return s.refreshHeld(ctx, site, hostname, s.routingFor(site))
}

// holdProject takes the project's lifecycle lease; ErrSiteBusy, saying the project is busy, while another operation holds it.
func (s *Service) holdProject(ctx context.Context, projectID string) (func(), error) {
	gate := s.options.Projects
	if gate == nil {
		return func() {}, nil
	}
	release, held, err := gate.HoldProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("hold the project's lifecycle: %w", err)
	}
	if !held {
		return nil, fmt.Errorf("%w: the project is busy with another operation; try again when it finishes", ErrSiteBusy)
	}
	return release, nil
}

// refreshHeld runs a check under the site's lease. An unproven claim only
// looks for its TXT proof; a proven one whose proof is gone leaves the zone.
func (s *Service) refreshHeld(ctx context.Context, site *Site, hostname *CustomHostname, route routing) (*CustomHostname, error) {
	ctx = context.WithoutCancel(ctx)
	if hostname.VerifiedAt == nil && hostname.WithdrawnAt != nil {
		if again := hostname.WithdrawnAt.Add(WithdrawnHostnameCooldown); s.now().Before(again) {
			return s.recordCheck(ctx, hostname, fmt.Sprintf("this domain was taken off at %s; it is proven again from %s",
				hostname.WithdrawnAt.UTC().Format(time.RFC3339), again.UTC().Format(time.RFC3339)))
		}
	}
	proof := s.lookupProof(ctx, hostname)
	if hostname.VerifiedAt == nil {
		if proof != proofSeen {
			return s.recordCheck(ctx, hostname, proofMissing(hostname, proof))
		}
		verified, err := s.verify(ctx, hostname)
		if err != nil {
			return nil, err
		}
		hostname = verified
	} else if proof == proofGone {
		return s.withdraw(ctx, site, hostname, fmt.Sprintf("the TXT record %s is gone; add it back to serve the domain again",
			VerificationName(hostname.Hostname)))
	}
	answer, err := s.readZone(ctx, hostname)
	if err != nil {
		return nil, s.recordZoneFailure(ctx, site, hostname, err)
	}
	return s.applyAnswer(ctx, site, hostname, answer, route)
}

// verify records the proof; the store refuses a name another row proved and a full zone.
func (s *Service) verify(ctx context.Context, hostname *CustomHostname) (*CustomHostname, error) {
	now := s.now().UTC()
	next := *hostname
	next.VerifiedAt, next.UpdatedAt = &now, now
	err := s.options.Hostnames.VerifyHostname(ctx, &next, s.maxOnZone())
	if errors.Is(err, ErrHostnameTaken) || errors.Is(err, ErrCustomDomainsFull) {
		if _, recordErr := s.recordCheck(ctx, hostname, err.Error()); recordErr != nil {
			log.Printf("site %s: record why %s was not proven: %v", hostname.SiteID, hostname.Hostname, recordErr)
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// recordCheck stores a check that changed nothing but what is still missing.
func (s *Service) recordCheck(ctx context.Context, hostname *CustomHostname, message string) (*CustomHostname, error) {
	now := s.now().UTC()
	next := *hostname
	next.CheckedAt, next.UpdatedAt, next.Errors = now, now, []string{message}
	next.Records = dnsInstructions(&next, s.options.CustomDomains.Target, cfworkers.CustomHostname{})
	if err := s.options.Hostnames.UpdateHostname(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// createRefused is Cloudflare refusing a create outright: nothing reached the zone.
type createRefused struct{ err error }

func (e *createRefused) Error() string { return e.err.Error() }
func (e *createRefused) Unwrap() error { return e.err }

// recordZoneFailure stores a failed check so the sweep moves on and callers
// stay throttled. A refused create releases the proof the way a withdraw does:
// off the router, off the CORS list, nothing left on the zone.
func (s *Service) recordZoneFailure(ctx context.Context, site *Site, hostname *CustomHostname, zoneErr error) error {
	var refused *createRefused
	var apiErr *cfworkers.APIError
	if errors.As(zoneErr, &refused) && errors.As(zoneErr, &apiErr) {
		if _, err := s.withdraw(ctx, site, hostname, "the CDN refused this domain: "+apiErr.Message); err != nil {
			log.Printf("site %s: take off %s, which the CDN refused: %v", site.ID, hostname.Hostname, err)
		}
		return publishFailure(site, stepCheckHostname, zoneErr)
	}
	now := s.now().UTC()
	next := *hostname
	next.CheckedAt, next.UpdatedAt = now, now
	next.Errors = []string{"the CDN could not be asked about this domain; it is checked again in a few minutes"}
	if err := s.options.Hostnames.UpdateHostname(ctx, &next); err != nil {
		log.Printf("site %s: record the failed check of %s: %v", site.ID, hostname.Hostname, err)
	}
	return publishFailure(site, stepCheckHostname, zoneErr)
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
	// The GET decides: only a hostname still pending validation is nudged, so
	// neither a failed nor an "active" PATCH can hide a moved or blocked one.
	if err != nil || hostnameStatus(answer) != HostnamePending {
		return answer, err
	}
	revalidated, err := zone.RevalidateCustomHostname(cdnCtx, hostname.CloudflareID, hostname.Validation)
	if err != nil {
		log.Printf("site %s: ask Cloudflare to validate %s again: %v", hostname.SiteID, hostname.Hostname, err)
		return answer, nil
	}
	return revalidated, nil
}

// createOnZone creates the proven hostname, adopting the zone's own copy when
// an earlier create got through but its answer was lost: the one proven row
// holds the name, so that copy is this row's.
func (s *Service) createOnZone(ctx context.Context, hostname *CustomHostname) (cfworkers.CustomHostname, error) {
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	zone := s.options.CustomDomains.Zone
	answer, err := zone.CreateCustomHostname(cdnCtx, hostname.Hostname, hostname.Validation)
	if definiteRefusal(err) {
		return cfworkers.CustomHostname{}, &createRefused{err: err}
	}
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

// definiteRefusal is Cloudflare answering that it will not create the
// hostname. A rate limit, like an outage, is transient: the next check tries again.
func definiteRefusal(err error) bool {
	var apiErr *cfworkers.APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 &&
		apiErr.Status != http.StatusTooManyRequests && !cfworkers.IsDuplicateCustomHostname(err)
}

// applyAnswer records Cloudflare's answer and routes an active hostname of a
// deployed site. One that stops being active leaves the zone and must be proven again.
func (s *Service) applyAnswer(ctx context.Context, site *Site, hostname *CustomHostname, answer cfworkers.CustomHostname, route routing) (*CustomHostname, error) {
	status := hostnameStatus(answer)
	if hostname.Status == HostnameActive && status != HostnameActive {
		reason := fmt.Sprintf("the CDN stopped serving this domain (%s, certificate %s); it is proven and added again on the next check",
			answer.Status, answer.SSL.Status)
		return s.withdraw(ctx, site, hostname, strings.Join(append([]string{reason}, hostnameErrors(answer)...), "; "))
	}
	now := s.now().UTC()
	next := *hostname
	if answer.ID != "" {
		next.CloudflareID = answer.ID
	}
	next.HostnameStatus, next.CertificateStatus, next.Status = answer.Status, answer.SSL.Status, status
	next.Errors = hostnameErrors(answer)
	next.Records = dnsInstructions(&next, s.options.CustomDomains.Target, answer)
	next.CheckedAt, next.UpdatedAt = now, now
	switch {
	case status == HostnameActive:
		next.InactiveSince = nil
	case next.InactiveSince == nil:
		next.InactiveSince = &now
	}
	if err := s.routeHostname(ctx, site, &next, hostname.Status, route); err != nil {
		return nil, err
	}
	if err := s.options.Hostnames.UpdateHostname(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// withdraw takes a proven hostname off the router and the zone and clears its
// proof: the next check must see the TXT record again before it returns.
func (s *Service) withdraw(ctx context.Context, site *Site, hostname *CustomHostname, reason string) (*CustomHostname, error) {
	if err := s.takeOffTheZone(ctx, site, hostname); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	next := *hostname
	next.VerifiedAt, next.CloudflareID, next.Status, next.WithdrawnAt = nil, "", HostnamePending, &now
	next.HostnameStatus, next.CertificateStatus = "", ""
	if next.InactiveSince == nil {
		next.InactiveSince = &now
	}
	next.CheckedAt, next.UpdatedAt, next.Errors = now, now, []string{reason}
	next.Records = dnsInstructions(&next, s.options.CustomDomains.Target, cfworkers.CustomHostname{})
	if err := s.options.Hostnames.UpdateHostname(ctx, &next); err != nil {
		return nil, err
	}
	log.Printf("site %s: custom domain %s withdrawn: %s", site.ID, hostname.Hostname, reason)
	return &next, nil
}

// routeHostname writes an active hostname into the router. Every caller holds
// the project's lifecycle lease, which a deletion holds from its unroute to
// its status write, so the serving check cannot fall between the two.
func (s *Service) routeHostname(ctx context.Context, site *Site, hostname *CustomHostname, previous string, route routing) error {
	if hostname.Status != HostnameActive {
		return nil
	}
	if previous != HostnameActive {
		if _, _, err := s.origins.AddCorsOrigin(ctx, site.ProjectID, "https://"+hostname.Hostname, HostnameCorsOwner(hostname)); err != nil {
			return fmt.Errorf("add the custom domain's origin to the project's CORS list: %w", err)
		}
	}
	if !route.live {
		return nil
	}
	serving, err := route.serving(ctx, site.ProjectID)
	if err != nil || !serving {
		return err
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := s.publisher.PutKV(cdnCtx, hostname.Hostname, ScriptName(site)); err != nil {
		return publishFailure(site, stepRoute, err)
	}
	return nil
}

func (s *Service) projectServing(ctx context.Context, projectID string) (bool, error) {
	gate := s.options.Projects
	if gate == nil {
		return true, nil
	}
	serving, err := gate.ProjectServing(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("read whether the project is being deleted: %w", err)
	}
	return serving, nil
}

// refreshSiteHostnames checks every proven hostname of the site before it is
// routed, rather than trusting what was stored. A failed check is recorded on
// the hostname and leaves it unrouted; the site itself still serves.
func (s *Service) refreshSiteHostnames(ctx context.Context, site *Site, route routing) error {
	if s.options.Hostnames == nil || s.options.CustomDomains == nil {
		return nil
	}
	held, err := s.options.Hostnames.ListHostnames(ctx, site.ProjectID, site.ID)
	if err != nil {
		return err
	}
	for _, hostname := range held {
		if hostname.VerifiedAt == nil {
			continue
		}
		if _, err := s.refreshHeld(ctx, site, hostname, route); err != nil {
			log.Printf("site %s: custom domain %s not routed: %v", site.ID, hostname.Hostname, err)
		}
	}
	return nil
}

// holdServingProject takes the project's lifecycle lease for a deploy or a
// rollback and refuses a project that may not serve. A deletion holds the
// lease from its unroute to its PENDING_DELETION write, so no route a publish
// writes can fall between the two.
func (s *Service) holdServingProject(ctx context.Context, projectID string) (func(), error) {
	release, err := s.holdProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	serving, err := s.projectServing(ctx, projectID)
	if err == nil && !serving {
		err = ErrProjectNotServing
	}
	if err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// routeAfterPublish checks and routes the site's custom hostnames; the publish holds the lifecycle lease.
func (s *Service) routeAfterPublish(ctx context.Context, site *Site) error {
	if s.options.Hostnames == nil || s.options.CustomDomains == nil {
		return nil
	}
	// The site's Worker exists now, whatever the stored site says.
	return s.refreshSiteHostnames(ctx, site, routing{serving: s.projectServing, live: true})
}
