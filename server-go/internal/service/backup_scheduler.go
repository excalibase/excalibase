package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// LeaderLock guards multi-replica backup firings. The scheduler only
// fires cron jobs when Acquire returns true. Concrete implementations:
// - PgAdvisoryLock (Postgres pg_try_advisory_lock) for cloud
// - alwaysLeader for self-hosted single-process deployments
type LeaderLock interface {
	Acquire(ctx context.Context) (acquired bool, err error)
	Release(ctx context.Context) error
}

// BackupSchedulerConfig wires the scheduler's collaborators.
type BackupSchedulerConfig struct {
	Schedules storage.BackupScheduleStore
	Backups   *BackupService
	Lock      LeaderLock
	// Logger is optional; defaults to the std log package.
	Logger *log.Logger
}

// BackupScheduler runs scheduled backups. It loads the persistent
// schedule on Start and replays each row into a robfig/cron job.
// New registrations are persisted then live-added without a restart.
type BackupScheduler struct {
	store   storage.BackupScheduleStore
	backups *BackupService
	lock    LeaderLock
	logger  *log.Logger

	mu     sync.Mutex
	cron   *cron.Cron
	jobs   map[string]cron.EntryID // projectID → cron entry id
	parser cron.Parser

	// running is true between Start and Stop; serialises double-start.
	running bool
}

func NewBackupScheduler(c BackupSchedulerConfig) *BackupScheduler {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &BackupScheduler{
		store:   c.Schedules,
		backups: c.Backups,
		lock:    c.Lock,
		logger:  logger,
		jobs:    make(map[string]cron.EntryID),
		parser:  cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor),
	}
}

// Register persists a schedule and adds it to the running cron.
func (s *BackupScheduler) Register(ctx context.Context, sched *domain.BackupSchedule) error {
	if _, err := s.parser.Parse(sched.Cron); err != nil {
		return fmt.Errorf("invalid cron %q: %w", sched.Cron, err)
	}
	if err := s.store.UpsertSchedule(ctx, sched); err != nil {
		return fmt.Errorf("persist schedule: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.replaceJobLocked(sched)
	}
	return nil
}

// Unregister removes the schedule from the store and the live cron.
func (s *BackupScheduler) Unregister(ctx context.Context, projectID string) error {
	if err := s.store.DeleteSchedule(ctx, projectID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.jobs[projectID]; ok {
		s.cron.Remove(id)
		delete(s.jobs, projectID)
	}
	return nil
}

// Start initialises the cron runtime and replays persistent schedules.
// Idempotent: subsequent calls are no-ops.
func (s *BackupScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.cron = cron.New(cron.WithParser(s.parser))
	s.running = true
	s.mu.Unlock()

	schedules, err := s.store.ListEnabledSchedules(ctx)
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}
	s.mu.Lock()
	for i := range schedules {
		s.replaceJobLocked(&schedules[i])
	}
	s.mu.Unlock()

	s.cron.Start()
	return nil
}

// Stop tears down the cron runtime cleanly.
func (s *BackupScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
		s.cron = nil
	}
	s.jobs = make(map[string]cron.EntryID)
	s.running = false
}

// JobCount returns the number of cron entries currently registered.
// Used by tests to assert replay-after-restart works correctly.
func (s *BackupScheduler) JobCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// replaceJobLocked re-binds the cron entry for a project. Caller
// must hold s.mu.
func (s *BackupScheduler) replaceJobLocked(sched *domain.BackupSchedule) {
	if id, ok := s.jobs[sched.ProjectID]; ok {
		s.cron.Remove(id)
		delete(s.jobs, sched.ProjectID)
	}
	if !sched.Enabled || s.cron == nil {
		return
	}
	projectID := sched.ProjectID
	id, err := s.cron.AddFunc(sched.Cron, func() { s.fireOne(projectID) })
	if err != nil {
		s.logger.Printf("scheduler: failed to add cron for %s: %v", projectID, err)
		return
	}
	s.jobs[projectID] = id
}

// fireOne is the per-project tick. It checks leadership before
// invoking the backup so multiple replicas don't double-fire.
func (s *BackupScheduler) fireOne(projectID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	acquired, err := s.lock.Acquire(ctx)
	if err != nil {
		s.logger.Printf("scheduler: leader lock error for %s: %v", projectID, err)
		return
	}
	if !acquired {
		// Another replica owns the lease; sit this tick out.
		return
	}
	defer func() { _ = s.lock.Release(ctx) }()

	if _, err := s.backups.TriggerManualBackup(ctx, projectID); err != nil {
		s.logger.Printf("scheduler: backup trigger %s failed: %v", projectID, err)
	}
}

// AlwaysLeader is a no-op LeaderLock for single-process self-hosted
// deployments where there's only ever one platform replica.
type AlwaysLeader struct{}

func (AlwaysLeader) Acquire(_ context.Context) (bool, error) { return true, nil }
func (AlwaysLeader) Release(_ context.Context) error         { return nil }
