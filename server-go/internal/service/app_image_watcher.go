package service

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// ImageWatcherActor is who the image watcher's deploys are recorded as.
const ImageWatcherActor = "image-watcher"

const (
	// maxImageWatchBackoff caps how long a failing check waits.
	maxImageWatchBackoff = time.Hour
	// imageWatchRateLimitWait is the least a registry that rate limited us is left alone.
	imageWatchRateLimitWait = time.Hour
)

const imageWatchUnknownFailure = "the check did not complete; it is retried"

// ImageWatcher deploys an app again when the tag it opted to follow points
// at a new digest (EXC-542). It asks each registry with a HEAD, which Docker
// Hub does not count as a pull, one app at a time, at most once per interval
// plus jitter, and backs off while a registry fails or rate limits.
type ImageWatcher struct {
	apps    apphost.ImageWatchStore
	deploys *AppDeployService
	every   time.Duration
	now     func() time.Time
	jitter  func(time.Duration) time.Duration

	mu       sync.Mutex
	schedule map[string]watchSchedule // keyed by app id
}

type watchSchedule struct {
	due      time.Time
	failures int
}

func NewImageWatcher(apps apphost.ImageWatchStore, deploys *AppDeployService, every time.Duration) *ImageWatcher {
	return &ImageWatcher{
		apps: apps, deploys: deploys, every: every,
		now: time.Now, jitter: imageWatchJitter,
		schedule: map[string]watchSchedule{},
	}
}

// imageWatchJitter spreads checks over a fifth of the interval so apps that
// opted in together do not all ask at once.
func imageWatchJitter(every time.Duration) time.Duration {
	spread := int64(every / 5)
	if spread <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(spread + 1))
}

// Start checks on a timer while this replica leads. Returns a function that stops it.
func (w *ImageWatcher) Start(ctx context.Context, leader LeaderChecker, tick time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.checkIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

func (w *ImageWatcher) checkIfLeader(ctx context.Context, leader LeaderChecker) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("image watcher: leadership check: %v", err)
		return
	}
	if leading {
		w.CheckDue(ctx)
	}
}

// CheckDue checks every opted-in app whose next check is due.
func (w *ImageWatcher) CheckDue(ctx context.Context) {
	apps, err := w.apps.ListAutoDeploy()
	if err != nil {
		log.Printf("image watcher: list apps: %v", err)
		return
	}
	listed := make(map[string]bool, len(apps))
	for _, app := range apps {
		listed[app.ID] = true
		if ctx.Err() != nil {
			return
		}
		if app.WatchesImage() && w.due(app.ID) {
			w.check(ctx, app)
		}
	}
	w.forgetUnlisted(listed)
}

func (w *ImageWatcher) due(appID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.now().Before(w.schedule[appID].due)
}

func (w *ImageWatcher) forgetUnlisted(listed map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for appID := range w.schedule {
		if !listed[appID] {
			delete(w.schedule, appID)
		}
	}
}

func (w *ImageWatcher) check(ctx context.Context, app *apphost.App) {
	digest, err := w.deploys.resolveImage(ctx, app.ProjectID, app.Image)
	if err != nil {
		w.failed(app, err)
		return
	}
	baseline := app.ResolvedDigest
	if baseline == "" && app.ImageWatch != nil {
		baseline = app.ImageWatch.Digest
	}
	w.record(app, apphost.ImageWatch{Digest: digest, CheckedAt: w.now()})
	w.succeeded(app.ID)
	if baseline == "" || digest == baseline {
		return
	}
	origin := apphost.DeployOrigin{Actor: ImageWatcherActor, Source: apphost.DeploySourceImageWatcher}
	if _, err := w.deploys.deployDigest(ctx, app.ProjectID, app.ID, app.Image, digest, origin); err != nil {
		log.Printf("image watcher: deploy %s/%s at %s: %v", app.ProjectID, app.ID, digest, err)
		w.failed(app, err)
	}
}

func (w *ImageWatcher) record(app *apphost.App, watch apphost.ImageWatch) {
	if err := w.apps.RecordImageWatch(app.ProjectID, app.ID, watch); err != nil && !errors.Is(err, apphost.ErrAppNotFound) {
		log.Printf("image watcher: record %s/%s: %v", app.ProjectID, app.ID, err)
	}
}

func (w *ImageWatcher) succeeded(appID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.schedule[appID] = watchSchedule{due: w.now().Add(w.every + w.jitter(w.every))}
}

// failed records why, in words safe to show, and doubles the wait from the
// interval up to maxImageWatchBackoff; a rate limit waits at least an hour.
func (w *ImageWatcher) failed(app *apphost.App, err error) {
	previous := apphost.ImageWatch{}
	if app.ImageWatch != nil {
		previous = *app.ImageWatch
	}
	w.record(app, apphost.ImageWatch{Digest: previous.Digest, CheckedAt: w.now(), Error: watchFailure(err)})

	w.mu.Lock()
	defer w.mu.Unlock()
	failures := w.schedule[app.ID].failures + 1
	wait := min(w.every<<min(failures, 16), maxImageWatchBackoff)
	if errors.Is(err, imagedigest.ErrRateLimited) {
		wait = max(wait, imageWatchRateLimitWait)
	}
	w.schedule[app.ID] = watchSchedule{due: w.now().Add(wait + w.jitter(w.every)), failures: failures}
}

func watchFailure(err error) string {
	for _, known := range []error{imagedigest.ErrNotFound, imagedigest.ErrDenied, imagedigest.ErrRateLimited,
		imagedigest.ErrUnavailable, imagedigest.ErrNotPublic, apphost.ErrAppBusy, apphost.ErrAppVersionConflict} {
		if errors.Is(err, known) {
			return err.Error()
		}
	}
	log.Printf("image watcher: %v", err)
	return imageWatchUnknownFailure
}
