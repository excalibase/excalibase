package sites

import (
	"context"
	"fmt"
)

// UnrouteProjectSites stops serving a project scheduled for deletion: the
// router forgets each hostname, while the Workers and their deploy history
// stay for a cancelled deletion.
func (s *Service) UnrouteProjectSites(ctx context.Context, projectID string) error {
	held, err := s.projectSitesForRouting(ctx, projectID)
	if err != nil {
		return err
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	for _, site := range held {
		if err := s.publisher.DeleteKV(cdnCtx, site.Hostname); err != nil {
			return publishFailure(site, stepUnroute, err)
		}
		if err := s.unrouteSiteHostnames(cdnCtx, site); err != nil {
			return err
		}
		if err := s.routeSitePreviews(cdnCtx, site, false); err != nil {
			return err
		}
	}
	return nil
}

// unrouteSiteHostnames forgets every proven custom hostname of the site in
// the router; they stay on the zone for a cancelled deletion. An unproven
// claim was never routed, and the name may be another project's.
func (s *Service) unrouteSiteHostnames(ctx context.Context, site *Site) error {
	if s.options.Hostnames == nil {
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
		if err := s.publisher.DeleteKV(ctx, hostname.Hostname); err != nil {
			return publishFailure(site, stepUnroute, err)
		}
	}
	return nil
}

// RouteProjectSites serves again every site of the project with a live deploy.
// The cancel calling it holds the project's lifecycle lease and has not yet
// written the project's status, so its custom hostnames are checked again
// and routed as if the project serves.
func (s *Service) RouteProjectSites(ctx context.Context, projectID string) error {
	held, err := s.projectSitesForRouting(ctx, projectID)
	if err != nil {
		return err
	}
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	for _, site := range held {
		if site.LiveDeployID == "" {
			continue
		}
		if err := s.publisher.PutKV(cdnCtx, site.Hostname, ScriptName(site)); err != nil {
			return publishFailure(site, stepRoute, err)
		}
		if err := s.routeSiteHostnames(cdnCtx, site); err != nil {
			return err
		}
	}
	for _, site := range held {
		if err := s.routeSitePreviews(cdnCtx, site, true); err != nil {
			return err
		}
	}
	return nil
}

// projectSitesForRouting lists the project's sites; holding any without a
// Cloudflare account to reach them is an error, never a silent success.
func (s *Service) projectSitesForRouting(ctx context.Context, projectID string) ([]*Site, error) {
	held, err := s.store.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(held) > 0 && !s.Available() {
		return nil, fmt.Errorf("%w: the project has %d sites this install cannot reach", ErrSitesUnavailable, len(held))
	}
	return held, nil
}

// routeSiteHostnames checks the site's custom hostnames under its lease, then routes the active ones.
func (s *Service) routeSiteHostnames(ctx context.Context, site *Site) error {
	if s.options.Hostnames == nil || s.options.CustomDomains == nil {
		return nil
	}
	lease, err := s.claim(ctx, site)
	if err != nil {
		return err
	}
	defer lease.release()
	return s.refreshSiteHostnames(ctx, site, routing{serving: assumeServing, live: true})
}
