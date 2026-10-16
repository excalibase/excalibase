package sites

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

const maxFailureReasonLength = 500

// DeployRequest is one upload of build output to a site.
type DeployRequest struct {
	ProjectID string
	SiteID    string
	Actor     string
	Label     string
	Body      io.Reader
}

// Deploy checks the archive, uploads what Cloudflare lacks, publishes the
// site's Worker and makes the deploy live. A deploy that fails is kept, FAILED.
func (s *Service) Deploy(ctx context.Context, request DeployRequest) (*Deploy, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if err := ValidateLabel(request.Label); err != nil {
		return nil, &InputError{Reason: err.Error()}
	}
	site, err := s.mustGet(ctx, request.ProjectID, request.SiteID)
	if err != nil {
		return nil, err
	}
	limits, err := s.limitsFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	bundle, err := ReadArchive(request.Body, limits, s.spoolDir())
	if err != nil {
		return nil, err
	}
	defer closeBundle(bundle, site)
	release, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.ensureOrigin(ctx, site); err != nil {
		return nil, err
	}
	deploy := s.newDeploy(site, bundle, request)
	if err := s.store.CreateDeploy(ctx, deploy); err != nil {
		return nil, err
	}
	publishErr := s.publish(ctx, site, deploy.Manifest, bundleAssets(bundle), deploy.SPAFallback)
	return s.finish(ctx, deploy, publishErr)
}

// Rollback publishes an earlier deploy's manifest again and makes it live.
func (s *Service) Rollback(ctx context.Context, projectID, siteID, deployID string) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	site, err := s.mustGet(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	target, err := s.GetDeploy(ctx, projectID, siteID, deployID)
	if err != nil {
		return nil, err
	}
	if target.Status != DeploySucceeded {
		return nil, ErrDeployNotServable
	}
	release, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer release()
	// File bodies are not kept here, so files Cloudflare asks for again cannot be sent.
	if err := s.publish(ctx, site, target.Manifest, nil, target.SPAFallback); err != nil {
		return nil, err
	}
	if err := s.store.SetLive(ctx, projectID, siteID, deployID); err != nil {
		return nil, err
	}
	return s.mustGet(ctx, projectID, siteID)
}

func (s *Service) spoolDir() string {
	if s.options.SpoolDir != "" {
		return s.options.SpoolDir
	}
	return os.TempDir()
}

func closeBundle(bundle *Bundle, site *Site) {
	if err := bundle.Close(); err != nil {
		log.Printf("site %s: remove deploy spool: %v", site.ID, err)
	}
}

// ensureOrigin offers the site's origin to the project's CORS list once. An
// origin already listed stays the user's, so deleting the site keeps it.
func (s *Service) ensureOrigin(ctx context.Context, site *Site) error {
	if site.CorsClaimed {
		return nil
	}
	if _, _, err := s.origins.AddCorsOrigin(ctx, site.ProjectID, Origin(site), CorsOwner(site)); err != nil {
		return fmt.Errorf("add the site's origin to the project's CORS list: %w", err)
	}
	return s.store.MarkCorsClaimed(ctx, site.ProjectID, site.ID)
}

func (s *Service) newDeploy(site *Site, bundle *Bundle, request DeployRequest) *Deploy {
	return &Deploy{
		ID: uuid.NewString(), SiteID: site.ID, ProjectID: site.ProjectID, Status: DeployPending,
		Manifest: bundle.Manifest(), FileCount: len(bundle.Files), TotalBytes: bundle.TotalBytes,
		SPAFallback: site.SPAFallback, Label: request.Label, CreatedBy: request.Actor, CreatedAt: s.now().UTC(),
	}
}

// finish records the outcome even when the caller has gone away.
func (s *Service) finish(ctx context.Context, deploy *Deploy, publishErr error) (*Deploy, error) {
	finishedAt := s.now().UTC()
	deploy.FinishedAt = &finishedAt
	deploy.Status = DeploySucceeded
	if publishErr != nil {
		deploy.Status = DeployFailed
		deploy.FailureReason = truncate(publishErr.Error(), maxFailureReasonLength)
	}
	if err := s.store.FinishDeploy(context.WithoutCancel(ctx), deploy); err != nil {
		return nil, fmt.Errorf("record the deploy's outcome: %w", err)
	}
	if publishErr != nil {
		return nil, publishErr
	}
	return deploy, nil
}

// publish runs Cloudflare's upload session, the bucket uploads and the
// script upload. assetsFor nil means no file body can be sent.
func (s *Service) publish(ctx context.Context, site *Site, manifest map[string]cfworkers.ManifestEntry,
	assetsFor func([]string) ([]cfworkers.Asset, error), spaFallback bool) error {
	script := ScriptName(site)
	session, err := s.publisher.StartUploadSession(ctx, script, manifest)
	if err != nil {
		return &PublishError{Err: err}
	}
	completion, err := s.uploadBuckets(ctx, session, assetsFor)
	if err != nil {
		return err
	}
	if err := s.publisher.PutScript(ctx, script, completion, assetConfig(spaFallback)); err != nil {
		return &PublishError{Err: err}
	}
	return nil
}

func (s *Service) uploadBuckets(ctx context.Context, session cfworkers.UploadSession,
	assetsFor func([]string) ([]cfworkers.Asset, error)) (string, error) {
	if len(session.Buckets) == 0 {
		return session.JWT, nil
	}
	if assetsFor == nil {
		return "", ErrRollbackNeedsFiles
	}
	completion := ""
	for _, bucket := range session.Buckets {
		assets, err := assetsFor(bucket)
		if err != nil {
			return "", err
		}
		token, err := s.publisher.UploadBucket(ctx, session.JWT, assets)
		if err != nil {
			return "", &PublishError{Err: err}
		}
		if token != "" {
			completion = token
		}
	}
	if completion == "" {
		return "", &PublishError{Err: fmt.Errorf("every file was sent but the upload was not confirmed")}
	}
	return completion, nil
}

// bundleAssets answers the bundle's files for the hashes Cloudflare asks for.
func bundleAssets(bundle *Bundle) func([]string) ([]cfworkers.Asset, error) {
	byHash := make(map[string]BundleFile, len(bundle.Files))
	for _, file := range bundle.Files {
		byHash[file.Hash] = file
	}
	return func(hashes []string) ([]cfworkers.Asset, error) {
		assets := make([]cfworkers.Asset, 0, len(hashes))
		for _, hash := range hashes {
			file, ok := byHash[hash]
			if !ok {
				return nil, &PublishError{Err: fmt.Errorf("the CDN asked for a file this deploy does not hold")}
			}
			assets = append(assets, cfworkers.Asset{
				Hash: hash, ContentType: file.ContentType,
				Open: func() (io.ReadCloser, error) { return bundle.Open(file) },
			})
		}
		return assets, nil
	}
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}
