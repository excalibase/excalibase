package service

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

const (
	// RegistryKeepDeploys is how many of an app's latest deploys keep their images, the running one besides.
	RegistryKeepDeploys = 10
	// RegistryPushGrace spares an image pushed this recently: its deploy may not be recorded yet.
	RegistryPushGrace = 24 * time.Hour
)

// RegistryAdmin is the registry as the sweep uses it.
type RegistryAdmin interface {
	Repositories(ctx context.Context) ([]string, error)
	Tags(ctx context.Context, repository string) ([]string, error)
	Delete(ctx context.Context, repository, digest string) error
}

// RegistrySweeper deletes built images nothing will run again (EXC-540): an
// app keeps its last RegistryKeepDeploys deploys' images and the one it names;
// a deleted app or project keeps none.
type RegistrySweeper struct {
	registry RegistryAdmin
	apps     apphost.Store
	deploys  apphost.DeployStore
	exists   appregistry.ProjectExists
	now      func() time.Time
}

// RegistrySweepReport counts one sweep.
type RegistrySweepReport struct {
	Repositories, Deleted, Failed int
}

func NewRegistrySweeper(registry RegistryAdmin, apps apphost.Store, deploys apphost.DeployStore, exists appregistry.ProjectExists) *RegistrySweeper {
	return &RegistrySweeper{registry: registry, apps: apps, deploys: deploys, exists: exists, now: time.Now}
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
	if err != nil {
		log.Printf("registry sweep: %v", err)
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
		keep, err := s.keep(ctx, repository)
		if err != nil {
			log.Printf("registry sweep: %s: %v", repository, err)
			report.Failed++
			continue
		}
		deleted, failed := s.sweepRepository(ctx, repository, keep)
		report.Deleted += deleted
		report.Failed += failed
	}
	return report, nil
}

// keep is the digests a repository must hold; empty for an app or project that is gone.
func (s *RegistrySweeper) keep(ctx context.Context, repository string) (map[string]bool, error) {
	projectID, appName, ok := appregistry.SplitRepository(repository)
	if !ok {
		return nil, appregistry.ErrInvalidName
	}
	keep := map[string]bool{}
	live, err := s.exists(ctx, projectID)
	if err != nil || !live {
		return keep, err
	}
	apps, err := s.apps.List(projectID)
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		if app.Name != appName {
			continue
		}
		keepDigest(keep, app.Image)
		deploys, err := s.deploys.ListByApp(projectID, app.ID, RegistryKeepDeploys)
		if err != nil {
			return nil, err
		}
		for _, deploy := range deploys {
			keepDigest(keep, deploy.Image)
			if deploy.Digest != "" {
				keep[deploy.Digest] = true
			}
		}
	}
	return keep, nil
}

func keepDigest(keep map[string]bool, image string) {
	if _, digest, pinned := strings.Cut(image, "@"); pinned {
		keep[digest] = true
	}
}

// sweepRepository deletes each pushed image outside keep and past the grace.
// A tag the platform did not write is left alone.
func (s *RegistrySweeper) sweepRepository(ctx context.Context, repository string, keep map[string]bool) (int, int) {
	tags, err := s.registry.Tags(ctx, repository)
	if err != nil {
		log.Printf("registry sweep: %s: list tags: %v", repository, err)
		return 0, 1
	}
	// A digest pushed twice is as young as its latest push.
	latest := map[string]time.Time{}
	for _, tag := range tags {
		if digest, pushed, ok := appregistry.ParsePushTag(tag); ok && pushed.After(latest[digest]) {
			latest[digest] = pushed
		}
	}
	deleted, failed := 0, 0
	for digest, pushed := range latest {
		if keep[digest] || s.now().Sub(pushed) < RegistryPushGrace {
			continue
		}
		if err := s.registry.Delete(ctx, repository, digest); err != nil {
			log.Printf("registry sweep: %s@%s: %v", repository, digest, err)
			failed++
			continue
		}
		deleted++
	}
	return deleted, failed
}
