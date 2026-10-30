package sites

import (
	"context"
	"errors"
	"fmt"
)

// previewSlotKey keeps previews' deploys in a budget of their own, so pull requests
// (forks included, when allowed) never use up the production branch's deploys an hour.
func previewSlotKey(projectID string) string { return projectID + "/previews" }

// TakePreviewSlot counts one preview build against the project's preview deploys this hour,
// which is as large as its deploys an hour and apart from them.
func (s *Service) TakePreviewSlot(ctx context.Context, projectID string) error {
	if !s.Available() {
		return ErrSitesUnavailable
	}
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return err
	}
	_, err = s.takeDeploySlot(ctx, previewSlotKey(projectID), limits.MaxDeploysPerHour)
	return err
}

// WithdrawPreviewAPIAccess takes every preview origin of the site off the project's CORS list now,
// as an Admin turning API access off expects; the previews stay served.
func (s *Service) WithdrawPreviewAPIAccess(ctx context.Context, projectID, siteID string) error {
	store, err := s.heldPreviews()
	if err != nil {
		return err
	}
	held, err := store.ListPreviews(ctx, projectID, siteID)
	if err != nil {
		return err
	}
	var failures []error
	for _, preview := range held {
		if preview.CorsClaimed {
			failures = append(failures, s.withdrawPreviewOrigin(ctx, store, preview))
		}
	}
	return errors.Join(failures...)
}

// withdrawPreviewOrigin holds the preview's lease, so a deploy cannot claim the origin again meanwhile.
func (s *Service) withdrawPreviewOrigin(ctx context.Context, store PreviewStore, preview *Preview) error {
	lease, err := s.claimPreview(ctx, store, preview, true)
	if err != nil {
		return fmt.Errorf("withdraw API access from preview %s: %w", preview.Hostname, err)
	}
	defer lease.release()
	if _, err := s.origins.ReleaseAppCorsOrigins(ctx, preview.ProjectID, previewCorsOwner(preview)); err != nil {
		return fmt.Errorf("release the preview's CORS origin: %w", err)
	}
	preview.CorsClaimed = false
	return store.FinishPreview(ctx, preview, lease.token)
}

// routeSitePreviews takes the site's previews off the router, or with serve puts back those that
// were deployed; a project scheduled for deletion serves none of them.
func (s *Service) routeSitePreviews(ctx context.Context, site *Site, serve bool) error {
	if !s.KeepsPreviews() {
		return nil
	}
	held, err := s.options.Previews.ListPreviews(ctx, site.ProjectID, site.ID)
	if err != nil {
		return err
	}
	for _, preview := range held {
		switch {
		case !serve:
			err = s.publisher.DeleteKV(ctx, preview.Hostname)
		case preview.DeployedSHA != "" && preview.Status != PreviewRemoving:
			err = s.publisher.PutKV(ctx, preview.Hostname, PreviewScriptName(preview))
		default:
			continue
		}
		if err != nil {
			return publishFailure(site, stepRoute, err)
		}
	}
	return nil
}
