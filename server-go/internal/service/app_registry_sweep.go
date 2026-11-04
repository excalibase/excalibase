package service

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/metrics"
)

const (
	// RegistryKeepDeploys is how many of an app's latest deploys keep their images, the running one besides.
	RegistryKeepDeploys = 10
	// RegistryPushGrace spares an image pushed this recently: its deploy may not be recorded yet.
	RegistryPushGrace = 24 * time.Hour
	// RegistryRecentPushes caps the images an app keeps inside the grace that no deploy names.
	RegistryRecentPushes = 3
	// RegistryPushFloor spares a push this recent from the cap too: a burst of
	// pushes whose deploys are still queued keeps every image.
	RegistryPushFloor = time.Hour
)

// ErrRollbackImageRemoved refuses a redeploy whose image the sweep or the
// quota has removed; the quota may take one inside RegistryKeepDeploys, so it promises no count.
var ErrRollbackImageRemoved = errors.New("that deploy's image was removed to keep this project within its registry space; build its commit again to run it")

// RegistryAdmin is the registry as the sweep uses it.
type RegistryAdmin interface {
	Repositories(ctx context.Context) ([]string, error)
	Tags(ctx context.Context, repository string) ([]string, error)
	Untag(ctx context.Context, repository, tag string) error
	Blobs(ctx context.Context, repository, reference string) (map[string]int64, error)
}

// RegistrySweeper removes the push tags of built images nothing will run
// again (EXC-540), and zot's own GC frees an image left with none. An app
// keeps the images of its last RegistryKeepDeploys deploys, its last deploy
// that succeeded and the one it names; a deleted app or project keeps none.
type RegistrySweeper struct {
	registry RegistryAdmin
	apps     apphost.Store
	deploys  apphost.DeployStore
	// live answers false only for a project that is gone for good: one whose
	// deletion can still be cancelled keeps its images.
	live appregistry.ProjectExists
	now  func() time.Time
	// countSweep records whether a pass judged anything.
	countSweep func(ok bool)
}

// RegistrySweepReport counts one sweep.
type RegistrySweepReport struct {
	Repositories, Deleted, Failed int
	// Unjudged is the repositories left whole because what they keep could not be read.
	Unjudged int
}

func NewRegistrySweeper(registry RegistryAdmin, apps apphost.Store, deploys apphost.DeployStore, live appregistry.ProjectExists) *RegistrySweeper {
	return &RegistrySweeper{registry: registry, apps: apps, deploys: deploys, live: live, now: time.Now,
		countSweep: metrics.CountRegistrySweep}
}

// Start sweeps at once and then every interval while this replica leads; the
// returned function stops it and waits for a sweep in flight.
func (s *RegistrySweeper) Start(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			s.sweepIfLeader(ctx, leader)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *RegistrySweeper) sweepIfLeader(ctx context.Context, leader LeaderChecker) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("registry sweep: leadership check: %v", err)
		return
	}
	if !leading {
		return
	}
	report, err := s.Sweep(ctx)
	failed := err != nil || (report.Repositories > 0 && report.Unjudged == report.Repositories)
	s.countSweep(!failed)
	switch {
	case err != nil:
		log.Printf("ERROR: registry sweep: %v", err)
	case failed:
		log.Printf("ERROR: registry sweep: none of %d repositories could be judged; nothing was deleted", report.Repositories)
	}
	if report.Deleted+report.Failed > 0 {
		log.Printf("registry sweep: %d repositories, %d images deleted, %d failed", report.Repositories, report.Deleted, report.Failed)
	}
}

// Sweep is one pass over every repository. A repository it cannot judge is
// left whole: deleting on a guess could take a running image.
func (s *RegistrySweeper) Sweep(ctx context.Context) (RegistrySweepReport, error) {
	repositories, err := s.registry.Repositories(ctx)
	if err != nil {
		return RegistrySweepReport{}, err
	}
	report := RegistrySweepReport{Repositories: len(repositories)}
	for _, repository := range repositories {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		one := s.sweepRepository(ctx, repository)
		report.Deleted += one.Deleted
		report.Failed += one.Failed
		report.Unjudged += one.Unjudged
	}
	return report, nil
}

// keep is the digests a repository must hold; empty for an app or project that is gone.
func (s *RegistrySweeper) keep(ctx context.Context, repository string) (map[string]bool, error) {
	keep := map[string]bool{}
	app, err := s.repositoryApp(ctx, repository)
	if err != nil || app == nil {
		return keep, err
	}
	return keep, keepAppImages(keep, s.deploys, app)
}

// repositoryApp is the app a repository holds images of; nil for an app or project that is gone.
func (s *RegistrySweeper) repositoryApp(ctx context.Context, repository string) (*apphost.App, error) {
	projectID, appName, ok := appregistry.SplitRepository(repository)
	if !ok {
		return nil, appregistry.ErrInvalidName
	}
	live, err := s.live(ctx, projectID)
	if err != nil || !live {
		return nil, err
	}
	apps, err := s.apps.List(projectID)
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		if app.Name == appName {
			return app, nil
		}
	}
	return nil, nil
}

// keepAppImages adds what an app keeps: the image it names, its last
// RegistryKeepDeploys deploys, and its last deploy that succeeded, which is
// what runs however many deploys failed after it.
func keepAppImages(keep map[string]bool, deploys apphost.DeployStore, app *apphost.App) error {
	keepDigest(keep, app.Image)
	recent, err := deploys.ListByApp(app.ProjectID, app.ID, RegistryKeepDeploys)
	if err != nil {
		return err
	}
	running, err := deploys.GetLastSucceeded(app.ProjectID, app.ID)
	if err != nil {
		return err
	}
	if running != nil {
		recent = append(recent, running)
	}
	for _, deploy := range recent {
		keepDeployImage(keep, deploy)
	}
	return nil
}

func keepDeployImage(keep map[string]bool, deploy *apphost.Deploy) {
	keepDigest(keep, deploy.Image)
	keepDigest(keep, "@"+deploy.Digest)
}

func keepDigest(keep map[string]bool, image string) {
	if _, digest, pinned := strings.Cut(image, "@"); pinned && digest != "" {
		keep[digest] = true
	}
}

// sweepRepository removes the push tags nothing keeps. Each tag goes alone,
// so a newer push of the same image keeps it; a tag the platform did not
// write is left. The keep set is read again just before anything goes, so a
// deploy recorded during the listing is not lost.
func (s *RegistrySweeper) sweepRepository(ctx context.Context, repository string) RegistrySweepReport {
	unjudged := RegistrySweepReport{Failed: 1, Unjudged: 1}
	keep, err := s.keep(ctx, repository)
	if err != nil {
		log.Printf("registry sweep: %s: %v", repository, err)
		return unjudged
	}
	tags, err := s.registry.Tags(ctx, repository)
	if err != nil {
		log.Printf("registry sweep: %s: list tags: %v", repository, err)
		return unjudged
	}
	stale := s.staleTags(tags, keep)
	if len(stale) == 0 {
		return RegistrySweepReport{}
	}
	if keep, err = s.keep(ctx, repository); err != nil {
		log.Printf("registry sweep: %s: %v", repository, err)
		return unjudged
	}
	return s.untag(ctx, repository, stale, keep)
}

func (s *RegistrySweeper) untag(ctx context.Context, repository string, stale []string, keep map[string]bool) RegistrySweepReport {
	var report RegistrySweepReport
	for _, tag := range stale {
		if digest, _, _ := appregistry.ParsePushTag(tag); keep[digest] {
			continue
		}
		if err := s.registry.Untag(ctx, repository, tag); err != nil {
			log.Printf("registry sweep: %s:%s: %v", repository, tag, err)
			report.Failed++
			continue
		}
		report.Deleted++
	}
	return report
}

// staleTags are the push tags of images nothing keeps: past the grace, or
// inside it beyond the RegistryRecentPushes newest and past the floor.
func (s *RegistrySweeper) staleTags(tags []string, keep map[string]bool) []string {
	type push struct {
		tag string
		at  time.Time
	}
	var recent []push
	var stale []string
	for _, tag := range tags {
		digest, at, ok := appregistry.ParsePushTag(tag)
		switch {
		case !ok || keep[digest]:
		case s.now().Sub(at) >= RegistryPushGrace:
			stale = append(stale, tag)
		default:
			recent = append(recent, push{tag, at})
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].at.After(recent[j].at) })
	for i, p := range recent {
		if i >= RegistryRecentPushes && s.now().Sub(p.at) >= RegistryPushFloor {
			stale = append(stale, p.tag)
		}
	}
	return stale
}
