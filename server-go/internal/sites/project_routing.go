package sites

import (
	"context"
	"errors"
	"fmt"
	"time"
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
		if site.LiveDeployID == "" || !s.servable(ctx, site) {
			continue
		}
		if err := s.publisher.PutKV(cdnCtx, site.Hostname, ScriptName(site)); err != nil {
			return publishFailure(site, stepRoute, err)
		}
		if err := s.routeSiteHostnames(cdnCtx, site); err != nil {
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

// reconcileSiteTimeout bounds routing one site on or off: a KV write and its custom hostnames.
const reconcileSiteTimeout = time.Minute

// servable is whether the router may send traffic to the site's Worker: a
// server site only while server sites are on, so switching them off stops their code.
func (s *Service) servable(ctx context.Context, site *Site) bool {
	return site.Kind != KindServer || s.serverSitesOn(ctx)
}

// ReconcileServerSites routes every live server site while server sites are
// on, and unroutes all of them while they are off. It runs at start and, while
// off, again on a timer, so a replica still on during a rollout cannot leave one
// routed. Every site is tried; the failures are joined.
func (s *Service) ReconcileServerSites(ctx context.Context) error {
	if !s.Available() {
		return nil
	}
	held, err := s.store.ListServerSites(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, site := range held {
		if err := s.reconcileInTime(ctx, site); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// reconcileInTime gives each site its own time, so one slow answer leaves no other site routed.
func (s *Service) reconcileInTime(ctx context.Context, site *Site) error {
	siteCtx, cancel := context.WithTimeout(ctx, reconcileSiteTimeout)
	defer cancel()
	return s.reconcileServerSite(siteCtx, site)
}

func (s *Service) reconcileServerSite(ctx context.Context, site *Site) error {
	if !s.serverSitesOn(ctx) {
		if err := s.publisher.DeleteKV(ctx, site.Hostname); err != nil {
			return publishFailure(site, stepUnroute, err)
		}
		return s.unrouteSiteHostnames(ctx, site)
	}
	serving, err := s.projectServing(ctx, site.ProjectID)
	if err != nil || !serving || site.LiveDeployID == "" {
		return err
	}
	if err := s.publisher.PutKV(ctx, site.Hostname, ScriptName(site)); err != nil {
		return publishFailure(site, stepRoute, err)
	}
	return s.routeSiteHostnames(ctx, site)
}
