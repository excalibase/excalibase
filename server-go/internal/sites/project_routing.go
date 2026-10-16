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
	}
	return nil
}

// RouteProjectSites serves again every site of the project with a live deploy.
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
