package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

const watchEvery = 5 * time.Minute

var movedDigest = "sha256:" + strings.Repeat("cd", 32)

// watchStore adds what the watcher reads to the deploy test's app store.
type watchStore struct {
	*fakeAppStoreForDeploy
	mu      sync.Mutex
	watches map[string]apphost.ImageWatch
}

func (s *watchStore) ListAutoDeploy() ([]*apphost.App, error) {
	all, _ := s.List(deployTestProject)
	out := []*apphost.App{}
	for _, app := range all {
		if app.AutoDeploy {
			out = append(out, app)
		}
	}
	return out, nil
}

func (s *watchStore) RecordImageWatch(projectID, id string, watch apphost.ImageWatch) error {
	s.fakeAppStoreForDeploy.mu.Lock()
	app, ok := s.apps[projectID+"/"+id]
	if ok {
		copied := watch
		app.ImageWatch = &copied
	}
	s.fakeAppStoreForDeploy.mu.Unlock()
	if !ok {
		return apphost.ErrAppNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watches[id] = watch
	return nil
}

func (s *watchStore) watchOf(id string) apphost.ImageWatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watches[id]
}

type watchLab struct {
	watcher  *ImageWatcher
	store    *watchStore
	deploys  *fakeDeployStore
	resolver *stubResolver
	clock    time.Time
}

func (l *watchLab) pass(t *testing.T, after time.Duration) {
	t.Helper()
	l.clock = l.clock.Add(after)
	l.watcher.CheckDue(context.Background())
}

func newWatchLab(t *testing.T, mutate func(*apphost.App)) *watchLab {
	t.Helper()
	app := sampleDeployApp()
	app.AutoDeploy = true
	app.Image = "ghcr.io/acme/storefront:main"
	app.Status = apphost.StatusRunning
	app.ResolvedDigest = resolvedDigest
	if mutate != nil {
		mutate(app)
	}
	svc, deploys, _ := newDeployTestService(t, app)
	store := &watchStore{fakeAppStoreForDeploy: svc.apps.(*fakeAppStoreForDeploy), watches: map[string]apphost.ImageWatch{}}
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	lab := &watchLab{store: store, deploys: deploys, resolver: resolver, clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	lab.watcher = NewImageWatcher(store, svc, watchEvery)
	lab.watcher.now = func() time.Time { return lab.clock }
	lab.watcher.jitter = func(time.Duration) time.Duration { return 0 }
	return lab
}

func (l *watchLab) asked() int {
	l.resolver.mu.Lock()
	defer l.resolver.mu.Unlock()
	return len(l.resolver.asked)
}

func TestImageWatcher_AnUnmovedTagDeploysNothing(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.pass(t, 0)
	if lab.asked() != 1 || len(lab.deploys.deploys) != 0 {
		t.Fatalf("asked %d, deploys %d", lab.asked(), len(lab.deploys.deploys))
	}
	watch := lab.store.watchOf(deployTestApp)
	if watch.Digest != resolvedDigest || !watch.CheckedAt.Equal(lab.clock) || watch.Error != "" {
		t.Fatalf("watch = %+v", watch)
	}
}

func TestImageWatcher_AMovedTagDeploysItsNewDigestOnce(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.resolver.digest = movedDigest
	lab.pass(t, 0)

	if len(lab.deploys.deploys) != 1 {
		t.Fatalf("deploys = %d, want 1", len(lab.deploys.deploys))
	}
	deploy := lab.deploys.deploys[0]
	if deploy.Source != apphost.DeploySourceImageWatcher || deploy.CreatedBy != ImageWatcherActor ||
		deploy.Image != "ghcr.io/acme/storefront@"+movedDigest || deploy.ImageRef != "ghcr.io/acme/storefront:main" {
		t.Fatalf("deploy = %+v", deploy)
	}
	if lab.asked() != 1 {
		t.Fatalf("the registry is asked once per check, not again to deploy: %d", lab.asked())
	}

	lab.pass(t, watchEvery)
	if len(lab.deploys.deploys) != 1 {
		t.Fatalf("the same digest must not deploy twice: %d deploys", len(lab.deploys.deploys))
	}
}

func TestImageWatcher_TheFirstLookIsTheBaseline(t *testing.T) {
	lab := newWatchLab(t, func(app *apphost.App) { app.ResolvedDigest = "" })
	lab.pass(t, 0)
	if len(lab.deploys.deploys) != 0 {
		t.Fatal("switching auto-deploy on must not deploy by itself")
	}
	lab.resolver.digest = movedDigest
	lab.pass(t, watchEvery)
	if len(lab.deploys.deploys) != 1 {
		t.Fatalf("a tag that moved after the baseline deploys: %d", len(lab.deploys.deploys))
	}
}

func TestImageWatcher_ChecksEachAppOncePerInterval(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.pass(t, 0)
	lab.pass(t, time.Minute)
	lab.pass(t, time.Minute)
	if lab.asked() != 1 {
		t.Fatalf("asked %d times inside one interval", lab.asked())
	}
	lab.pass(t, watchEvery)
	if lab.asked() != 2 {
		t.Fatalf("asked %d times after the interval", lab.asked())
	}
}

func TestImageWatcher_SkipsWhatItMustNotDeploy(t *testing.T) {
	for name, mutate := range map[string]func(*apphost.App){
		"stopped":          func(app *apphost.App) { app.Status = apphost.StatusStopped },
		"pinned by digest": func(app *apphost.App) { app.Image = "ghcr.io/acme/storefront@" + resolvedDigest },
		"never deployed":   func(app *apphost.App) { app.Status = apphost.StatusCreated },
	} {
		lab := newWatchLab(t, mutate)
		lab.resolver.digest = movedDigest
		lab.pass(t, 0)
		if lab.asked() != 0 || len(lab.deploys.deploys) != 0 {
			t.Errorf("%s: asked %d, deploys %d", name, lab.asked(), len(lab.deploys.deploys))
		}
	}
}

func TestImageWatcher_BacksOffWhileTheRegistryFails(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.resolver.err = imagedigest.ErrUnavailable
	lab.pass(t, 0)
	if watch := lab.store.watchOf(deployTestApp); !strings.Contains(watch.Error, "did not answer") {
		t.Fatalf("the failure is recorded for the app's developers: %+v", watch)
	}
	lab.pass(t, watchEvery)
	if lab.asked() != 1 {
		t.Fatalf("after one failure the next check waits twice the interval; asked %d", lab.asked())
	}
	lab.pass(t, watchEvery)
	if lab.asked() != 2 {
		t.Fatalf("asked %d after twice the interval", lab.asked())
	}
	lab.pass(t, 3*watchEvery)
	if lab.asked() != 2 {
		t.Fatalf("after two failures the wait doubles again; asked %d", lab.asked())
	}

	lab.resolver.err = nil
	lab.pass(t, watchEvery)
	if lab.asked() != 3 || lab.store.watchOf(deployTestApp).Error != "" {
		t.Fatalf("an answer clears the failure: asked %d, watch %+v", lab.asked(), lab.store.watchOf(deployTestApp))
	}
	lab.pass(t, watchEvery)
	if lab.asked() != 4 {
		t.Fatalf("after an answer the interval is back to normal; asked %d", lab.asked())
	}
}

func TestImageWatcher_ARateLimitWaitsAnHour(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.resolver.err = imagedigest.ErrRateLimited
	lab.pass(t, 0)
	lab.pass(t, 59*time.Minute)
	if lab.asked() != 1 {
		t.Fatalf("asked %d inside the hour after a rate limit", lab.asked())
	}
	lab.pass(t, time.Minute)
	if lab.asked() != 2 {
		t.Fatalf("asked %d after the hour", lab.asked())
	}
}

func TestImageWatcher_TheBackoffIsCapped(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.resolver.err = imagedigest.ErrUnavailable
	for range 10 {
		lab.pass(t, maxImageWatchBackoff)
	}
	if lab.asked() != 10 {
		t.Fatalf("a check never waits longer than %s; asked %d of 10", maxImageWatchBackoff, lab.asked())
	}
}

func TestImageWatcher_AnUnreadableCredentialIsNeverAnAnonymousRequest(t *testing.T) {
	lab := newWatchLab(t, nil)
	registries, vault, _ := newRegistryService(t)
	vault.getErr = errors.New("vault sealed")
	lab.watcher.deploys.SetRegistryCredentials(registries)
	lab.pass(t, 0)
	if lab.asked() != 0 {
		t.Fatal("the registry was asked without the saved credential")
	}
	if watch := lab.store.watchOf(deployTestApp); watch.Error == "" || strings.Contains(watch.Error, "vault sealed") {
		t.Fatalf("the failure is recorded without internal detail: %+v", watch)
	}
}

func TestImageWatcher_AsksWithTheSavedCredential(t *testing.T) {
	lab := newWatchLab(t, nil)
	registries, _, _ := newRegistryService(t)
	if _, err := registries.Set(deployTestProject, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	lab.watcher.deploys.SetRegistryCredentials(registries)
	lab.pass(t, 0)
	if lab.resolver.creds[0] == nil || lab.resolver.creds[0].Password != registryTestPassword {
		t.Fatalf("asked with %+v", lab.resolver.creds[0])
	}
}

type flagLeader struct{ leading bool }

func (l flagLeader) IsLeader(context.Context) (bool, error) { return l.leading, nil }

func TestImageWatcher_OnlyTheLeaderChecks(t *testing.T) {
	lab := newWatchLab(t, nil)
	lab.watcher.checkIfLeader(context.Background(), flagLeader{leading: false})
	if lab.asked() != 0 {
		t.Fatal("a replica that does not lead must not ask any registry")
	}
	lab.watcher.checkIfLeader(context.Background(), flagLeader{leading: true})
	if lab.asked() != 1 {
		t.Fatalf("the leader checks: asked %d", lab.asked())
	}
}

func TestImageWatcher_JitterSpreadsChecks(t *testing.T) {
	for range 100 {
		got := imageWatchJitter(watchEvery)
		if got < 0 || got > watchEvery/5 {
			t.Fatalf("jitter %s outside [0, %s]", got, watchEvery/5)
		}
	}
}
