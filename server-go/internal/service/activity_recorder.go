package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// DefaultActivityWindow is how often a hot project may hit the database
// with a last-seen write. Idle-pause thresholds are measured in days, so
// minute-level precision is plenty.
const DefaultActivityWindow = 5 * time.Minute

// DefaultActivityMaxTracked bounds the in-memory throttle map. Project ids
// arrive from URLs, so an unbounded map keyed by them would be a memory
// sink for anyone spraying random ids at public routes.
const DefaultActivityMaxTracked = 10_000

const activityWriteTimeout = 2 * time.Second

// ActivityRecorderConfig wires the recorder. Only Store is required.
type ActivityRecorderConfig struct {
	Store storage.ProjectActivityStore
	// Window is the minimum gap between two writes for the same project.
	Window time.Duration
	// MaxTracked caps the throttle map; projects beyond the cap are still
	// written, just not throttled.
	MaxTracked int
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// ActivityRecorder is the write-side of project activity: it collapses the
// stream of "project X was seen" events into at most one database write per
// project per window. Safe for concurrent use.
type ActivityRecorder struct {
	store      storage.ProjectActivityStore
	window     time.Duration
	maxTracked int
	now        func() time.Time

	mu        sync.Mutex
	lastWrite map[string]time.Time
}

func NewActivityRecorder(c ActivityRecorderConfig) *ActivityRecorder {
	rec := &ActivityRecorder{
		store:      c.Store,
		window:     c.Window,
		maxTracked: c.MaxTracked,
		now:        c.Now,
		lastWrite:  make(map[string]time.Time),
	}
	if rec.window <= 0 {
		rec.window = DefaultActivityWindow
	}
	if rec.maxTracked <= 0 {
		rec.maxTracked = DefaultActivityMaxTracked
	}
	if rec.now == nil {
		rec.now = time.Now
	}
	return rec
}

// Window returns the effective throttle window.
func (r *ActivityRecorder) Window() time.Duration { return r.window }

// Tracked returns how many projects currently sit in the throttle map.
func (r *ActivityRecorder) Tracked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lastWrite)
}

// Record notes that projectID was seen now via source. It returns without
// touching the store when a write already happened inside the window. A
// failed write does not consume the window, so the next call retries.
// Only the canonical literal of source (never the request path it was
// classified from) reaches the row and the warning log.
func (r *ActivityRecorder) Record(ctx context.Context, projectID string, source domain.ActivitySource) {
	if projectID == "" || r.store == nil {
		return
	}
	now := r.now()
	if !r.claimWindow(projectID, now) {
		return
	}
	sourceName := source.String()
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), activityWriteTimeout)
	defer cancel()
	if err := r.store.TouchProjectActivity(writeCtx, projectID, sourceName, now); err != nil {
		log.Printf("WARN: project activity write failed (source=%s): %v", sourceName, err)
		r.releaseWindow(projectID)
	}
}

// claimWindow reports whether a write is due and, if so, stamps the map so
// concurrent callers for the same project sit the window out.
func (r *ActivityRecorder) claimWindow(projectID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.lastWrite[projectID]; ok {
		if now.Sub(last) < r.window {
			return false
		}
		r.lastWrite[projectID] = now
		return true
	}
	if len(r.lastWrite) >= r.maxTracked {
		r.sweepExpiredLocked(now)
	}
	if len(r.lastWrite) < r.maxTracked {
		r.lastWrite[projectID] = now
	}
	return true
}

func (r *ActivityRecorder) releaseWindow(projectID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lastWrite, projectID)
}

// sweepExpiredLocked drops entries whose window has elapsed. Caller holds r.mu.
func (r *ActivityRecorder) sweepExpiredLocked(now time.Time) {
	for id, last := range r.lastWrite {
		if now.Sub(last) >= r.window {
			delete(r.lastWrite, id)
		}
	}
}
