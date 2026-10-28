package sites

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

// DeployRequest is one upload of build output to a site.
type DeployRequest struct {
	ProjectID string
	SiteID    string
	Actor     string
	Label     string
	Body      io.Reader
	// SlotTaken is a git build's deploy: the build took its slot of the hourly
	// limit when it was queued (EXC-354). Never set from a request.
	SlotTaken bool
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
	if err := s.requireKind(ctx, site.Kind); err != nil {
		return nil, err
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
	giveBackSlot := func() {}
	if !request.SlotTaken {
		giveBackSlot, err = s.takeDeploySlot(ctx, request.ProjectID, limits.MaxDeploysPerHour)
		if err != nil {
			return nil, err
		}
	}
	bundle, err := s.readBundle(site, request.Body, limits)
	if err != nil {
		return nil, err
	}
	defer closeBundle(bundle, site)
	lease, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	released, err := s.holdServingProject(ctx, site.ProjectID)
	if err != nil {
		// Refused for the project's lifecycle, not the upload: the attempt does not count.
		giveBackSlot()
		return nil, err
	}
	defer released()
	if err := s.ensureOrigin(ctx, site); err != nil {
		return nil, err
	}
	deploy := s.newDeploy(site, bundle, request)
	if token := DeployTokenFrom(ctx); token != nil {
		deploy.DeployTokenID, deploy.DeployTokenName = token.ID, token.Name
	}
	if err := s.store.CreateDeploy(ctx, deploy); err != nil {
		return nil, err
	}
	swapped := false
	publishErr := s.keepCode(ctx, deploy, bundle)
	if publishErr == nil {
		swapped, publishErr = s.publishInTime(ctx, site, newRelease(deploy, bundleAssets(bundle), bundleModules(bundle), limits))
	}
	finished, err := s.finish(ctx, deploy, lease, publishErr)
	// A deploy that failed after its Worker changed is what the CDN runs; its code stays.
	if publishErr == nil || !swapped {
		s.pruneCode(ctx, deploy, limits)
	}
	return finished, err
}

// readBundle reads the archive in the layout of the site's kind.
func (s *Service) readBundle(site *Site, body io.Reader, limits Limits) (*Bundle, error) {
	if site.Kind == KindServer {
		return ReadServerArchive(body, limits, s.spoolDir())
	}
	return ReadArchive(body, limits, s.spoolDir())
}

// keepCode stores a server deploy's modules before anything is published, so a live deploy always has its code.
func (s *Service) keepCode(ctx context.Context, deploy *Deploy, bundle *Bundle) error {
	if deploy.Kind != KindServer {
		return nil
	}
	packed, err := packModules(bundle)
	if err == nil {
		err = s.options.Code.PutCode(ctx, deploy, packed)
	}
	if err != nil {
		// The cause names our database; the deploy records only the fixed reason.
		log.Printf("site %s: keep server code: %v", deploy.SiteID, err)
		return ErrCodeNotKept
	}
	return nil
}

// pruneCode drops code past the rollback window; a failure only keeps more than needed.
func (s *Service) pruneCode(ctx context.Context, deploy *Deploy, limits Limits) {
	if deploy.Kind != KindServer {
		return
	}
	err := s.options.Code.PruneCode(context.WithoutCancel(ctx), deploy.ProjectID, deploy.SiteID, ServerCodeKept, ServerCodeBudget(limits))
	if err != nil {
		log.Printf("site %s: prune kept server code: %v", deploy.SiteID, err)
	}
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
	if err := s.requireKind(ctx, target.Kind); err != nil {
		return nil, err
	}
	limits, err := s.limitsFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// A rollback publishes like an upload, so it shares the in-flight cap.
	admitted, err := s.admission.admit(projectID)
	if err != nil {
		return nil, err
	}
	defer admitted()
	// A server rollback uploads all its code again, so it counts as a deploy.
	if target.Kind == KindServer {
		if _, err := s.takeDeploySlot(ctx, projectID, limits.MaxDeploysPerHour); err != nil {
			return nil, err
		}
	}
	lease, err := s.claim(ctx, site)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	released, err := s.holdServingProject(ctx, site.ProjectID)
	if err != nil {
		return nil, err
	}
	defer released()
	// Read under the lease, so no deploy's prune can drop it meanwhile.
	modules, err := s.keptModules(ctx, target)
	if err != nil {
		return nil, err
	}
	// Asset bodies are not kept here, so files Cloudflare asks for again cannot be sent.
	if _, err := s.publishInTime(ctx, site, newRelease(target, nil, modules, limits)); err != nil {
		return nil, err
	}
	// The CDN already switched; the record follows even if the caller has gone.
	if err := s.store.SetLive(context.WithoutCancel(ctx), projectID, siteID, deployID, lease.token); err != nil {
		return nil, err
	}
	return s.mustGet(context.WithoutCancel(ctx), projectID, siteID)
}

// keptModules reads a server deploy's code back for a rollback; a static deploy has none.
func (s *Service) keptModules(ctx context.Context, target *Deploy) ([]cfworkers.Module, error) {
	if target.Kind != KindServer {
		return nil, nil
	}
	packed, err := s.options.Code.GetCode(ctx, target.ProjectID, target.SiteID, target.ID)
	if err != nil {
		return nil, err
	}
	return unpackModules(packed)
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
	deploy := &Deploy{
		ID: uuid.NewString(), SiteID: site.ID, ProjectID: site.ProjectID, Status: DeployPending, Kind: site.Kind,
		Manifest: bundle.Manifest(), FileCount: len(bundle.Files), TotalBytes: bundle.TotalBytes,
		SPAFallback: site.SPAFallback, Label: request.Label, CreatedBy: request.Actor, CreatedAt: s.now().UTC(),
	}
	if bundle.Server != nil {
		deploy.Server = &ServerDeploy{ServerConfig: *bundle.Server, Modules: len(bundle.Modules), CodeBytes: bundle.CodeBytes}
	}
	return deploy
}

// finish records the outcome even when the caller has gone away. A deploy
// whose lease was taken over is recorded FAILED and never goes live.
func (s *Service) finish(ctx context.Context, deploy *Deploy, lease *siteLease, publishErr error) (*Deploy, error) {
	finishedAt := s.now().UTC()
	deploy.FinishedAt = &finishedAt
	deploy.Status = DeploySucceeded
	if publishErr != nil {
		deploy.Status = DeployFailed
		deploy.FailureReason = publishErr.Error()
	}
	err := s.store.FinishDeploy(context.WithoutCancel(ctx), deploy, lease.token)
	if errors.Is(err, ErrLeaseLost) {
		deploy.Status, deploy.FailureReason, publishErr = DeployFailed, ErrLeaseLost.Error(), ErrLeaseLost
		err = s.store.FinishDeploy(context.WithoutCancel(ctx), deploy, lease.token)
	}
	if err != nil {
		return nil, fmt.Errorf("record the deploy's outcome: %w", err)
	}
	if publishErr != nil {
		return nil, publishErr
	}
	return deploy, nil
}

// release is what one publish sends: a deploy's assets and, for a server deploy, its code.
type release struct {
	deploy *Deploy
	// assetsFor nil means no file body can be sent.
	assetsFor func([]string) ([]cfworkers.Asset, error)
	modules   []cfworkers.Module
	// limits are the plan's now; a rollback runs under today's plan, not the deploy's.
	limits Limits
}

func newRelease(deploy *Deploy, assetsFor func([]string) ([]cfworkers.Asset, error), modules []cfworkers.Module, limits Limits) release {
	return release{deploy: deploy, assetsFor: assetsFor, modules: modules, limits: limits}
}

// version is the Worker the release publishes, given Cloudflare's completion token.
func (r release) version(completion string) (cfworkers.ScriptVersion, error) {
	if r.deploy.Kind != KindServer {
		return cfworkers.ScriptVersion{AssetsJWT: completion, Assets: assetConfig(r.deploy.SPAFallback)}, nil
	}
	server := r.deploy.Server
	if server == nil {
		return cfworkers.ScriptVersion{}, fmt.Errorf("server deploy %s has no recorded settings", r.deploy.ID)
	}
	// Unknown paths must reach the code, which renders its own 404.
	return cfworkers.ScriptVersion{
		AssetsJWT:          completion,
		Assets:             cfworkers.AssetConfig{HTMLHandling: cfworkers.HTMLAutoTrailingSlash, NotFoundHandling: cfworkers.NotFoundNone},
		MainModule:         server.Main,
		Modules:            r.modules,
		CompatibilityDate:  server.CompatibilityDate,
		CompatibilityFlags: server.CompatibilityFlags,
		Limits:             &cfworkers.ScriptLimits{CPUMillis: r.limits.MaxServerCPUMillis, Subrequests: r.limits.MaxServerSubrequests},
	}, nil
}

// publishInTime bounds every Cloudflare call by publishTimeout, inside the
// lease. swapped reports that the Worker already runs the release, even if a later step failed.
func (s *Service) publishInTime(ctx context.Context, site *Site, rel release) (swapped bool, err error) {
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return s.publish(publishCtx, site, rel)
}

// publish runs Cloudflare's upload session, the bucket uploads and the script upload.
func (s *Service) publish(ctx context.Context, site *Site, rel release) (bool, error) {
	script := ScriptName(site)
	session, err := s.publisher.StartUploadSession(ctx, script, rel.deploy.Manifest)
	if err != nil {
		return false, publishFailure(site, stepStartUpload, err)
	}
	completion, err := s.uploadBuckets(ctx, site, session, rel.assetsFor)
	if err != nil {
		return false, err
	}
	version, err := rel.version(completion)
	if err != nil {
		return false, err
	}
	if err := s.publisher.PutScript(ctx, script, version); err != nil {
		return false, publishFailure(site, stepPublish, err)
	}
	// Written on every publish: idempotent, and it heals a missing entry.
	if err := s.publisher.PutKV(ctx, site.Hostname, script); err != nil {
		return true, publishFailure(site, stepRoute, err)
	}
	return true, s.routeAfterPublish(ctx, site)
}

func (s *Service) uploadBuckets(ctx context.Context, site *Site, session cfworkers.UploadSession,
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
			return "", publishFailure(site, stepUploadFiles, err)
		}
		if token != "" {
			completion = token
		}
	}
	if completion == "" {
		return "", publishFailure(site, stepUploadFiles, fmt.Errorf("every file was sent but the upload was not confirmed"))
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
				return nil, &PublishError{Step: stepUploadFiles, Err: fmt.Errorf("the CDN asked for a file this deploy does not hold")}
			}
			assets = append(assets, cfworkers.Asset{
				Hash: hash, ContentType: file.ContentType,
				Open: func() (io.ReadCloser, error) { return bundle.Open(file) },
			})
		}
		return assets, nil
	}
}
