package sites

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/google/uuid"
)

func newPreviewID() string { return uuid.NewString() }

// PreviewDeployRequest is one build's output for a preview.
type PreviewDeployRequest struct {
	ProjectID string
	SiteID    string
	PreviewID string
	CommitSHA string
	// APIAccess adds the preview's origin to the project's CORS list; off by default (EXC-660).
	APIAccess bool
	Body      io.Reader
}

// previewLease is one held claim on a preview; release survives a caller that went away.
type previewLease struct {
	token   string
	release func()
}

func (s *Service) claimPreview(ctx context.Context, store PreviewStore, preview *Preview, removing bool) (*previewLease, error) {
	token := uuid.NewString()
	if err := store.ClaimPreview(ctx, preview.ProjectID, preview.ID, token, s.now().Add(MaxDeployDuration), removing); err != nil {
		return nil, err
	}
	return &previewLease{token: token, release: func() {
		if err := store.ReleasePreview(context.WithoutCancel(ctx), preview.ProjectID, preview.ID, token); err != nil {
			log.Printf("preview %s: release lease: %v", preview.ID, err)
		}
	}}, nil
}

// DeployPreview checks the archive like any deploy and publishes it as the preview's own Worker
// at the preview's hostname. The site's live deploy, custom domains and CORS list stay untouched
// unless APIAccess asks for the preview's origin.
func (s *Service) DeployPreview(ctx context.Context, request PreviewDeployRequest) (*Preview, error) {
	store, err := s.previewStore()
	if err != nil {
		return nil, err
	}
	site, err := s.mustGet(ctx, request.ProjectID, request.SiteID)
	if err != nil {
		return nil, err
	}
	preview, err := store.GetPreview(ctx, request.ProjectID, request.SiteID, request.PreviewID)
	if err != nil {
		return nil, err
	}
	if preview == nil || preview.Status == PreviewRemoving {
		return nil, ErrPreviewNotFound
	}
	limits, err := s.limitsFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	admitted, err := s.admission.admit(request.ProjectID)
	if err != nil {
		return nil, err
	}
	defer admitted()
	bundle, err := ReadArchive(request.Body, limits, s.spoolDir())
	if err != nil {
		return nil, err
	}
	defer closeBundle(bundle, site)
	lease, err := s.claimPreview(ctx, store, preview, false)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	released, err := s.holdServingProject(ctx, site.ProjectID)
	if err != nil {
		return nil, err
	}
	defer released()
	publishErr := s.publishPreview(ctx, site, preview, bundle, request.APIAccess)
	return s.finishPreview(ctx, store, preview, bundle, request.CommitSHA, lease, publishErr)
}

func (s *Service) publishPreview(ctx context.Context, site *Site, preview *Preview, bundle *Bundle, apiAccess bool) error {
	if apiAccess && !preview.CorsClaimed {
		if _, _, err := s.origins.AddCorsOrigin(ctx, site.ProjectID, preview.URL(), previewCorsOwner(preview)); err != nil {
			return fmt.Errorf("add the preview's origin to the project's CORS list: %w", err)
		}
		preview.CorsClaimed = true
	}
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	script := PreviewScriptName(preview)
	session, err := s.publisher.StartUploadSession(publishCtx, script, bundle.Manifest())
	if err != nil {
		return publishFailure(site, stepStartUpload, err)
	}
	completion, err := s.uploadBuckets(publishCtx, site, session, bundleAssets(bundle))
	if err != nil {
		return err
	}
	if err := s.publisher.PutScript(publishCtx, script, completion, assetConfig(site.SPAFallback)); err != nil {
		return publishFailure(site, stepPublish, err)
	}
	if err := s.publisher.PutKV(publishCtx, preview.Hostname, script); err != nil {
		return publishFailure(site, stepRoute, err)
	}
	return nil
}

// finishPreview records the outcome even when the caller has gone away.
func (s *Service) finishPreview(ctx context.Context, store PreviewStore, preview *Preview, bundle *Bundle, commit string,
	lease *previewLease, publishErr error) (*Preview, error) {
	now := s.now().UTC()
	preview.UpdatedAt = now
	if publishErr != nil {
		preview.Status, preview.FailureReason = PreviewFailed, publicReason(publishErr)
	} else {
		preview.Status, preview.FailureReason, preview.DeployedSHA = PreviewReady, "", commit
		preview.FileCount, preview.TotalBytes, preview.DeployedAt = len(bundle.Files), bundle.TotalBytes, &now
	}
	if err := store.FinishPreview(context.WithoutCancel(ctx), preview, lease.token); err != nil {
		return nil, fmt.Errorf("record the preview's outcome: %w", err)
	}
	if publishErr != nil {
		return nil, publishErr
	}
	return preview, nil
}

// publicReason keeps Cloudflare's own words (which name our account) out of what users read.
func publicReason(err error) string {
	var publish *PublishError
	if errors.As(err, &publish) {
		return publish.Error()
	}
	return "the preview could not be published; push again"
}

// RecordPreviewFailure marks a preview whose build failed before anything was published.
func (s *Service) RecordPreviewFailure(ctx context.Context, projectID, siteID, previewID, reason string) error {
	store, err := s.previewStore()
	if err != nil {
		return err
	}
	preview, err := store.GetPreview(ctx, projectID, siteID, previewID)
	if err != nil || preview == nil || preview.Status == PreviewRemoving {
		return err
	}
	lease, err := s.claimPreview(ctx, store, preview, false)
	if err != nil {
		return err
	}
	defer lease.release()
	preview.Status, preview.FailureReason, preview.UpdatedAt = PreviewFailed, reason, s.now().UTC()
	return store.FinishPreview(context.WithoutCancel(ctx), preview, lease.token)
}

// RemovePreview takes the preview off the router, deletes its Worker (its assets go with it),
// releases its CORS origin and forgets it. A failure leaves it REMOVING for the sweep.
func (s *Service) RemovePreview(ctx context.Context, projectID, siteID, previewID string) error {
	store, err := s.previewStore()
	if err != nil {
		return err
	}
	preview, err := store.GetPreview(ctx, projectID, siteID, previewID)
	if err != nil {
		return err
	}
	if preview == nil {
		return ErrPreviewNotFound
	}
	return s.removePreview(ctx, store, preview)
}

// RemovePreviewByRef removes the preview of a closed pull request or deleted branch; none is not an error.
func (s *Service) RemovePreviewByRef(ctx context.Context, projectID, siteID, source, ref string) error {
	store, err := s.previewStore()
	if err != nil {
		return err
	}
	preview, err := store.FindPreview(ctx, projectID, siteID, source, ref)
	if err != nil || preview == nil {
		return err
	}
	return s.removePreview(ctx, store, preview)
}

func (s *Service) removePreview(ctx context.Context, store PreviewStore, preview *Preview) error {
	if err := store.MarkPreviewRemoving(ctx, preview.ProjectID, preview.ID); err != nil {
		return err
	}
	lease, err := s.claimPreview(ctx, store, preview, true)
	if err != nil {
		return err
	}
	defer lease.release()
	cdnCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	site := &Site{ID: preview.SiteID, ProjectID: preview.ProjectID}
	if err := s.publisher.DeleteKV(cdnCtx, preview.Hostname); err != nil {
		return publishFailure(site, stepUnroute, err)
	}
	if err := s.publisher.DeleteScript(cdnCtx, PreviewScriptName(preview)); err != nil {
		return publishFailure(site, stepDelete, err)
	}
	if _, err := s.origins.ReleaseAppCorsOrigins(ctx, preview.ProjectID, previewCorsOwner(preview)); err != nil {
		return fmt.Errorf("release the preview's CORS origin: %w", err)
	}
	return store.DeletePreview(ctx, preview.ProjectID, preview.ID, lease.token)
}

// removeSitePreviews runs before a site is deleted; any preview left on Cloudflare keeps the site.
func (s *Service) removeSitePreviews(ctx context.Context, site *Site) error {
	store := s.options.Previews
	if store == nil {
		return nil
	}
	held, err := store.ListPreviews(ctx, site.ProjectID, site.ID)
	if err != nil {
		return err
	}
	for _, preview := range held {
		if err := s.removePreview(ctx, store, preview); err != nil {
			return fmt.Errorf("remove the site's preview %s: %w", preview.Hostname, err)
		}
	}
	return nil
}

// PreviewSweepReport is what one pass over previews did.
type PreviewSweepReport struct {
	Removed int
	Failed  int
}

// SweepPreviews removes previews idle past PreviewLifetime and retries removals that failed.
// A preview busy with a deploy is left for the next pass.
func (s *Service) SweepPreviews(ctx context.Context) PreviewSweepReport {
	report := PreviewSweepReport{}
	store, err := s.previewStore()
	if err != nil {
		return report
	}
	due, err := store.DuePreviews(ctx, s.now().Add(-PreviewLifetime), previewSweepBatch)
	if err != nil {
		log.Printf("sites: list previews due removal: %v", err)
		report.Failed++
		return report
	}
	for _, preview := range due {
		err := s.removePreview(ctx, store, preview)
		switch {
		case err == nil:
			report.Removed++
		case errors.Is(err, ErrSiteBusy):
		default:
			log.Printf("sites: remove preview %s: %v", preview.ID, err)
			report.Failed++
		}
	}
	return report
}
