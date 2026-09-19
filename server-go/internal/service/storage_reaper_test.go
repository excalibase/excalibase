package service

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// fakeUploadReaper records the arguments each sweep was called with.
type fakeUploadReaper struct {
	calls  []reapCall
	report storagesvc.ReapReport
	err    error
}

type reapCall struct {
	grace time.Duration
	now   time.Time
}

func (f *fakeUploadReaper) ReapUnconfirmedUploads(_ context.Context, grace time.Duration, now time.Time) (storagesvc.ReapReport, error) {
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
