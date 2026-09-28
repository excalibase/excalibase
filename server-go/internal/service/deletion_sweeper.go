package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	// DefaultDeletionSweepInterval is how often due deletions and due backup
	// purges are looked for. The dates are days apart, so this only bounds how
	// late past its date a project or backup set goes.
	DefaultDeletionSweepInterval = 15 * time.Minute
	deletionSweepTickBudget      = 30 * time.Minute
)

// DeletionSweepWork is the pair of sweeps the ticker drives. ProvisioningService
// implements it.
type DeletionSweepWork interface {
	RunDueDeletions(ctx context.Context) DeletionSweepReport
	PurgeDueRetainedBackups(ctx context.Context) RetainedBackupSweepReport
}

// DeletionSweeperConfig wires the sweeper. Work and Lock are required.
type DeletionSweeperConfig struct {
	Work     DeletionSweepWork
	Lock     storage.LeaderLock
	Interval time.Duration
	Logger   *log.Logger
}

// DeletionSweeper hard-deletes projects whose grace period has ended and
// purges kept backups whose retention has ended, on the leading replica only.
type DeletionSweeper struct {
	work       DeletionSweepWork
	leadership *Leadership
	interval   time.Duration
	logger     *log.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// NewDeletionSweeper builds the sweeper; it does nothing until Start.
func NewDeletionSweeper(c DeletionSweeperConfig) *DeletionSweeper {
	s := &DeletionSweeper{work: c.Work, leadership: NewLeadership(c.Lock), interval: c.Interval, logger: c.Logger}
	if s.interval <= 0 {
		s.interval = DefaultDeletionSweepInterval
	}
	if s.logger == nil {
		s.logger = log.Default()
	}
	return s
}

// Start launches the periodic sweep. Idempotent.
func (s *DeletionSweeper) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done, s.running = cancel, make(chan struct{}), true
	go s.loop(runCtx, s.done)
}

// Stop halts the sweep, waits for an in-flight tick and hands leadership back.
func (s *DeletionSweeper) Stop() {
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
	if err := s.leadership.Close(context.Background()); err != nil {
		s.logger.Printf("deletion-sweep: releasing leadership: %v", err)
	}
}

func (s *DeletionSweeper) loop(ctx context.Context, done chan struct{}) {
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

func (s *DeletionSweeper) tick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, deletionSweepTickBudget)
	defer cancel()
	leader, err := s.leadership.IsLeader(ctx)
	if err != nil {
		s.logger.Printf("deletion-sweep: leader lock error: %v", err)
		return
	}
	if !leader {
		return
	}
	deletions := s.work.RunDueDeletions(ctx)
	purges := s.work.PurgeDueRetainedBackups(ctx)
	if len(deletions.Deleted)+len(deletions.Failed)+len(purges.Purged)+len(purges.Failed) > 0 {
		s.logger.Printf("deletion-sweep: deleted=%d failed=%d backups_purged=%d backups_failed=%d",
			len(deletions.Deleted), len(deletions.Failed), len(purges.Purged), len(purges.Failed))
	}
}
