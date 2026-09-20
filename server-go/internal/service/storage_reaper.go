package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

const (
	// DefaultStorageReapInterval is how often the sweep runs. Abandoned
	// uploads cost money but are not urgent, so hourly is enough.
	DefaultStorageReapInterval = time.Hour
	// storageReapTickBudget bounds one sweep so a slow object store cannot
	// hold the ticker open indefinitely.
	storageReapTickBudget = 15 * time.Minute
)

// UploadReaper is the slice of the storage service the sweep needs.
type UploadReaper interface {
	ReapUnconfirmedUploads(ctx context.Context, grace time.Duration, now time.Time) (storagesvc.ReapReport, error)
}

// StorageReaperConfig wires the sweep. Storage and Lock are required.
type StorageReaperConfig struct {
	Storage UploadReaper
	Lock    storage.LeaderLock
	// Grace is how long an unconfirmed object is left alone; defaults to
	// storagesvc.DefaultUnconfirmedGrace.
	Grace time.Duration
	// Interval between sweeps; defaults to DefaultStorageReapInterval.
	Interval time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now    func() time.Time
	Logger *log.Logger
}

// StorageReaper deletes objects that reached the blob plane but were never
// confirmed. They carry no catalogue row, so quota accounting cannot see them
// and nothing else would ever remove them.
//
// Shaped after IdlePauseScheduler: one leader-guarded tick per interval, with
// the work itself in a RunOnce that tests drive directly.
type StorageReaper struct {
	storage    UploadReaper
	leadership *Leadership
	grace      time.Duration
	interval   time.Duration
	now        func() time.Time
	logger     *log.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func NewStorageReaper(c StorageReaperConfig) *StorageReaper {
	s := &StorageReaper{
		storage: c.Storage, leadership: NewLeadership(c.Lock),
		grace: c.Grace, interval: c.Interval, now: c.Now, logger: c.Logger,
	}
	if s.grace <= 0 {
		s.grace = storagesvc.DefaultUnconfirmedGrace
	}
	if s.interval <= 0 {
		s.interval = DefaultStorageReapInterval
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.logger == nil {
		s.logger = log.Default()
	}
	return s
}

// Start launches the periodic sweep. Idempotent.
func (s *StorageReaper) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done, s.running = cancel, make(chan struct{}), true
	go s.loop(runCtx, s.done)
}

// Stop halts the sweep and waits for an in-flight tick to finish.
func (s *StorageReaper) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancel, s.done
	s.running = false
	s.mu.Unlock()
	cancel()
	<-done
	// Hand the claim back so another replica leads immediately.
	if err := s.leadership.Close(context.Background()); err != nil {
		s.logger.Printf("storage-reaper: releasing leadership: %v", err)
	}
}

func (s *StorageReaper) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one leader-guarded sweep with a bounded budget.
func (s *StorageReaper) tick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, storageReapTickBudget)
	defer cancel()
	leader, err := s.leadership.IsLeader(ctx)
	if err != nil {
		s.logger.Printf("storage-reaper: leader lock error: %v", err)
		return
	}
	if !leader {
		return
	}
	report, err := s.RunOnce(ctx)
	if err != nil {
		s.logger.Printf("storage-reaper: sweep failed: %v", err)
		return
	}
	if len(report.Deleted)+len(report.Failed) > 0 {
		s.logger.Printf("storage-reaper: deleted=%d failed=%d", len(report.Deleted), len(report.Failed))
	}
}

// RunOnce sweeps once without touching the leader lock.
func (s *StorageReaper) RunOnce(ctx context.Context) (storagesvc.ReapReport, error) {
	return s.storage.ReapUnconfirmedUploads(ctx, s.grace, s.now())
}
