package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeActivityStore counts writes so tests can prove the throttle holds, and
// keeps the idle-warning marker so the scheduler tests can drive it.
type fakeActivityStore struct {
	mu      sync.Mutex
	touches []touch
	warned  map[string]time.Time
	err     error
}

type touch struct {
	projectID, source string
	at                time.Time
}

func (f *fakeActivityStore) TouchProjectActivity(_ context.Context, projectID, source string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.touches = append(f.touches, touch{projectID, source, at})
	delete(f.warned, projectID)
	return nil
}

func (f *fakeActivityStore) MarkIdleWarned(_ context.Context, projectID string, lastSeen, warnedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.latestLocked(projectID); !ok {
		f.touches = append(f.touches, touch{projectID, "created", lastSeen})
	}
	if f.warned == nil {
		f.warned = map[string]time.Time{}
	}
	f.warned[projectID] = warnedAt
	return nil
}

func (f *fakeActivityStore) latestLocked(projectID string) (domain.ProjectActivity, bool) {
	for i := len(f.touches) - 1; i >= 0; i-- {
		if f.touches[i].projectID == projectID {
			row := domain.ProjectActivity{ProjectID: projectID, LastSeenAt: f.touches[i].at, LastSeenSource: f.touches[i].source}
			if w, ok := f.warned[projectID]; ok {
				row.IdleWarnedAt = &w
			}
			return row, true
		}
	}
	return domain.ProjectActivity{}, false
}

func (f *fakeActivityStore) GetProjectActivity(_ context.Context, projectID string) (domain.ProjectActivity, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.latestLocked(projectID)
	return row, ok, nil
}

func (f *fakeActivityStore) ListProjectActivity(_ context.Context) (map[string]domain.ProjectActivity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]domain.ProjectActivity{}
	for _, t := range f.touches {
		out[t.projectID], _ = f.latestLocked(t.projectID)
	}
	return out, nil
}

func (f *fakeActivityStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touches)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newRecorderUnderTest(store *fakeActivityStore, window time.Duration) (*ActivityRecorder, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	rec := NewActivityRecorder(ActivityRecorderConfig{Store: store, Window: window, Now: clock.Now})
	return rec, clock
}

func TestActivityRecorder_OneWritePerWindow(t *testing.T) {
	store := &fakeActivityStore{}
	rec, clock := newRecorderUnderTest(store, 5*time.Minute)
	ctx := context.Background()

	rec.Record(ctx, "p1", "api")
	rec.Record(ctx, "p1", "api")
	clock.Advance(5*time.Minute - time.Second)
	rec.Record(ctx, "p1", "api")
	if got := store.count(); got != 1 {
		t.Fatalf("writes inside the window: got %d, want 1", got)
	}

	clock.Advance(time.Second)
	rec.Record(ctx, "p1", "functions")
	if got := store.count(); got != 2 {
		t.Fatalf("write after the window elapsed: got %d, want 2", got)
	}
	last := store.touches[1]
	if last.source != "functions" || !last.at.Equal(clock.Now()) {
		t.Errorf("second write carries the new source + timestamp: %+v", last)
	}
}

func TestActivityRecorder_ProjectsThrottledIndependently(t *testing.T) {
	store := &fakeActivityStore{}
	rec, _ := newRecorderUnderTest(store, 5*time.Minute)
	ctx := context.Background()

	rec.Record(ctx, "p1", "api")
	rec.Record(ctx, "p2", "api")
	rec.Record(ctx, "p1", "api")
	if got := store.count(); got != 2 {
		t.Errorf("one write per project: got %d, want 2", got)
	}
}

func TestActivityRecorder_FailedWriteIsRetriedNextCall(t *testing.T) {
	store := &fakeActivityStore{err: errors.New("db down")}
	rec, _ := newRecorderUnderTest(store, 5*time.Minute)
	ctx := context.Background()

	rec.Record(ctx, "p1", "api")
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	rec.Record(ctx, "p1", "api")
	if got := store.count(); got != 1 {
		t.Errorf("a failed write must not consume the window: got %d writes, want 1", got)
	}
}

func TestActivityRecorder_IgnoresEmptyProject(t *testing.T) {
	store := &fakeActivityStore{}
	rec, _ := newRecorderUnderTest(store, time.Minute)
	rec.Record(context.Background(), "", "api")
	if store.count() != 0 {
		t.Error("empty project id must not be recorded")
	}
}

func TestActivityRecorder_BoundedTrackingStillWritesThrough(t *testing.T) {
	store := &fakeActivityStore{}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rec := NewActivityRecorder(ActivityRecorderConfig{Store: store, Window: time.Minute, Now: clock.Now, MaxTracked: 2})
	ctx := context.Background()

	rec.Record(ctx, "p1", "api")
	rec.Record(ctx, "p2", "api")
	rec.Record(ctx, "p3", "api") // over capacity: written, not tracked
	rec.Record(ctx, "p3", "api")
	if got := store.count(); got != 4 {
		t.Errorf("untracked projects write through: got %d, want 4", got)
	}
	if got := rec.Tracked(); got != 2 {
		t.Errorf("tracking map must stay bounded: got %d, want 2", got)
	}

	clock.Advance(2 * time.Minute)
	rec.Record(ctx, "p4", "api") // expired entries are swept, p4 fits
	if got := rec.Tracked(); got != 1 {
		t.Errorf("expired entries swept before admitting a new project: got %d, want 1", got)
	}
}

func TestActivityRecorder_DefaultsWhenConfigIsSparse(t *testing.T) {
	store := &fakeActivityStore{}
	rec := NewActivityRecorder(ActivityRecorderConfig{Store: store})
	rec.Record(context.Background(), "p1", "api")
	rec.Record(context.Background(), "p1", "api")
	if store.count() != 1 {
		t.Errorf("default window must throttle: got %d writes", store.count())
	}
	if rec.Window() != DefaultActivityWindow {
		t.Errorf("default window: got %v", rec.Window())
	}
}
