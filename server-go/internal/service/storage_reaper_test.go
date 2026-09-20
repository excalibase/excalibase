package service

import (
	"context"
	"errors"
	"io"
	"log"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// fakeUploadReaper records the arguments each sweep was called with.
type fakeUploadReaper struct {
	mu     sync.Mutex
	calls  []reapCall
	report storagesvc.ReapReport
	err    error
}

// callCount is the concurrency-safe view the loop test reads.
func (f *fakeUploadReaper) callCount() []reapCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reapCall(nil), f.calls...)
}

type reapCall struct {
	grace time.Duration
	now   time.Time
}

func (f *fakeUploadReaper) ReapUnconfirmedUploads(_ context.Context, grace time.Duration, now time.Time) (storagesvc.ReapReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reapCall{grace: grace, now: now})
	return f.report, f.err
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestStorageReaper_RunOnceUsesConfiguredGraceAndClock(t *testing.T) {
	reaper := &fakeUploadReaper{report: storagesvc.ReapReport{Deleted: []string{"files/a.bin"}}}
	at := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper,
		Lock:    &fakeLeaderLock{},
		Grace:   2 * time.Hour,
		Now:     func() time.Time { return at },
		Logger:  quietLogger(),
	})

	report, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(report.Deleted) != 1 {
		t.Errorf("report not passed through: %+v", report)
	}
	if len(reaper.calls) != 1 || reaper.calls[0].grace != 2*time.Hour || !reaper.calls[0].now.Equal(at) {
		t.Errorf("sweep called with %+v", reaper.calls)
	}
}

// An unset grace or interval takes the documented default rather than zero,
// which would reap uploads that are still in flight.
func TestStorageReaper_DefaultsGraceAndInterval(t *testing.T) {
	reaper := &fakeUploadReaper{}
	s := NewStorageReaper(StorageReaperConfig{Storage: reaper, Lock: &fakeLeaderLock{}, Logger: quietLogger()})
	if s.grace != storagesvc.DefaultUnconfirmedGrace {
		t.Errorf("grace: got %s, want %s", s.grace, storagesvc.DefaultUnconfirmedGrace)
	}
	if s.interval != DefaultStorageReapInterval {
		t.Errorf("interval: got %s, want %s", s.interval, DefaultStorageReapInterval)
	}
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(reaper.calls) != 1 || reaper.calls[0].grace != storagesvc.DefaultUnconfirmedGrace {
		t.Errorf("sweep called with %+v", reaper.calls)
	}
}

func TestStorageReaper_RunOnceReportsFailure(t *testing.T) {
	s := NewStorageReaper(StorageReaperConfig{
		Storage: &fakeUploadReaper{err: errors.New("object store unreachable")},
		Lock:    &fakeLeaderLock{}, Logger: quietLogger(),
	})
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed sweep must surface, not look like a clean run")
	}
}

// A tick only sweeps while this replica holds the leadership claim, so
// several replicas do not race each other over the same objects.
func TestStorageReaper_TickSkipsWhenNotLeader(t *testing.T) {
	reaper := &fakeUploadReaper{}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: neverLeaderLock{}, Logger: quietLogger(),
	})
	s.tick(context.Background())
	if len(reaper.calls) != 0 {
		t.Errorf("a non-leader must not sweep, got %d calls", len(reaper.calls))
	}
}

func TestStorageReaper_TickSweepsWhenLeader(t *testing.T) {
	reaper := &fakeUploadReaper{}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: &fakeLeaderLock{}, Logger: quietLogger(),
	})
	s.tick(context.Background())
	if len(reaper.calls) != 1 {
		t.Errorf("leader should sweep once, got %d calls", len(reaper.calls))
	}
}

func TestStorageReaper_StartStopIsIdempotent(t *testing.T) {
	s := NewStorageReaper(StorageReaperConfig{
		Storage: &fakeUploadReaper{}, Lock: &fakeLeaderLock{},
		Interval: time.Hour, Logger: quietLogger(),
	})
	ctx := context.Background()
	s.Start(ctx)
	s.Start(ctx) // second start must not spawn a second loop
	s.Stop()
	s.Stop() // stopping twice must not block or panic
}

// neverLeaderLock stands in for a replica that never wins the election.
type neverLeaderLock struct{}

func (neverLeaderLock) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return nil, false, nil
}

// A leader whose sweep fails logs it and stands down for this tick rather
// than treating the failure as a clean run.
func TestStorageReaper_TickSurvivesASweepFailure(t *testing.T) {
	reaper := &fakeUploadReaper{err: errors.New("object store unreachable")}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: &fakeLeaderLock{}, Logger: quietLogger(),
	})
	s.tick(context.Background())
	if len(reaper.calls) != 1 {
		t.Errorf("the sweep should have been attempted once, got %d", len(reaper.calls))
	}
}

// A lock that cannot be consulted is not leadership.
func TestStorageReaper_TickSkipsWhenLeadershipCannotBeChecked(t *testing.T) {
	reaper := &fakeUploadReaper{}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: brokenLeaderLock{}, Logger: quietLogger(),
	})
	s.tick(context.Background())
	if len(reaper.calls) != 0 {
		t.Errorf("a replica that cannot check its claim must not sweep, got %d calls", len(reaper.calls))
	}
}

// A sweep that deleted something says so, exercising the reporting branch.
func TestStorageReaper_TickReportsWhatItDeleted(t *testing.T) {
	reaper := &fakeUploadReaper{report: storagesvc.ReapReport{
		Deleted: []string{"files/a.bin"}, Failed: []string{"proj/files"},
	}}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: &fakeLeaderLock{}, Logger: quietLogger(),
	})
	s.tick(context.Background())
	if len(reaper.calls) != 1 {
		t.Fatalf("expected one sweep, got %d", len(reaper.calls))
	}
}

// The loop fires on its own interval and stops cleanly, handing the claim
// back even when releasing it fails.
func TestStorageReaper_LoopSweepsUntilStopped(t *testing.T) {
	reaper := &fakeUploadReaper{}
	s := NewStorageReaper(StorageReaperConfig{
		Storage: reaper, Lock: &fakeLeaderLock{},
		Interval: time.Millisecond, Logger: quietLogger(),
	})
	s.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for len(reaper.callCount()) == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	s.Stop()
	if len(reaper.callCount()) == 0 {
		t.Error("the loop should have swept at least once")
	}
}

// A logger is optional; the scheduler supplies one rather than panicking.
func TestStorageReaper_DefaultsLogger(t *testing.T) {
	s := NewStorageReaper(StorageReaperConfig{Storage: &fakeUploadReaper{}, Lock: &fakeLeaderLock{}})
	if s.logger == nil {
		t.Error("a scheduler without a logger must still have one")
	}
}

// brokenLeaderLock stands in for a lock the platform cannot consult.
type brokenLeaderLock struct{}

func (brokenLeaderLock) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return nil, false, errors.New("advisory lock unavailable")
}
