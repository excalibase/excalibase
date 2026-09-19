package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeScheduleStore is an in-memory BackupScheduleStore.
type fakeScheduleStore struct {
	mu        sync.Mutex
	schedules []domain.BackupSchedule
}

func (f *fakeScheduleStore) UpsertSchedule(_ context.Context, s *domain.BackupSchedule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.schedules {
		if existing.ProjectID == s.ProjectID {
			f.schedules[i] = *s
			return nil
		}
	}
	f.schedules = append(f.schedules, *s)
	return nil
}

func (f *fakeScheduleStore) ListEnabledSchedules(_ context.Context) ([]domain.BackupSchedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.BackupSchedule{}
	for _, s := range f.schedules {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeScheduleStore) DeleteSchedule(_ context.Context, projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.schedules[:0]
	for _, s := range f.schedules {
		if s.ProjectID != projectID {
			out = append(out, s)
		}
	}
	f.schedules = out
	return nil
}

// fakeLeaderLock hands out leases, counting how often the lock itself was
// asked — a standing claim asks once, not once per job.
type fakeLeaderLock struct {
	mu        sync.Mutex
	acquired  bool
	acquires  int
	refuse    bool
	leaseDead bool
}

func (l *fakeLeaderLock) Acquire(_ context.Context) (storage.LeaderLease, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquires++
	if l.refuse {
		return nil, false, nil
	}
	l.acquired = true
	return &fakeLease{lock: l, dead: l.leaseDead}, true, nil
}

func (l *fakeLeaderLock) acquireCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquires
}

type fakeLease struct {
	lock *fakeLeaderLock
	dead bool
}

func (le *fakeLease) Release(context.Context) error {
	le.lock.mu.Lock()
	defer le.lock.mu.Unlock()
	le.lock.acquired = false
	return nil
}

func (le *fakeLease) Valid(context.Context) bool { return !le.dead }

func setupScheduler(t *testing.T) (*BackupScheduler, *fakeScheduleStore, *storage.FileSystemStore, *fakeAdapter, *fakeLeaderLock) {
	t.Helper()
	dir := t.TempDir()
	instances, _ := storage.NewFileSystemStore(dir)
	schedules := &fakeScheduleStore{}
	adapter := &fakeAdapter{}
	lock := &fakeLeaderLock{}
	bsvc := NewBackupServiceWithAdapters(instances, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeDocker: adapter,
	}, dir)
	scheduler := NewBackupScheduler(BackupSchedulerConfig{
		Schedules: schedules,
		Backups:   bsvc,
		Lock:      lock,
	})
	return scheduler, schedules, instances, adapter, lock
}

func TestScheduler_RegisterAndRun(t *testing.T) {
	scheduler, schedules, instances, adapter, _ := setupScheduler(t)
	instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})

	// 6-field cron lets us schedule per-second for the test.
	if err := scheduler.Register(context.Background(), &domain.BackupSchedule{
		ProjectID: "p1", Cron: "@every 1s", RetentionDays: 7, Enabled: true,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, _ := schedules.ListEnabledSchedules(context.Background())
	if len(got) != 1 {
		t.Errorf("schedule not persisted: %d", len(got))
	}

	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(scheduler.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		adapter.muCount.Lock()
		n := adapter.triggerCalls
		adapter.muCount.Unlock()
		if n >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("scheduler never fired adapter (triggerCalls=%d)", adapter.triggerCalls)
}

func TestScheduler_InvalidCronReturnsError(t *testing.T) {
	scheduler, _, _, _, _ := setupScheduler(t)
	err := scheduler.Register(context.Background(), &domain.BackupSchedule{
		ProjectID: "p1", Cron: "not-a-cron", Enabled: true,
	})
	if err == nil {
		t.Error("expected error for bad cron")
	}
}

func TestScheduler_PersistsAcrossRestart(t *testing.T) {
	scheduler1, schedules, instances, _, _ := setupScheduler(t)
	instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})
	scheduler1.Register(context.Background(), &domain.BackupSchedule{
		ProjectID: "p1", Cron: "0 0 * * *", RetentionDays: 7, Enabled: true,
	})

	// "Restart" — new scheduler with same store.
	bsvc := NewBackupServiceWithAdapters(instances, nil, "")
	scheduler2 := NewBackupScheduler(BackupSchedulerConfig{
		Schedules: schedules,
		Backups:   bsvc,
		Lock:      &fakeLeaderLock{},
	})
	if err := scheduler2.Start(context.Background()); err != nil {
		t.Fatalf("Start after restart: %v", err)
	}
	t.Cleanup(scheduler2.Stop)

	if got := scheduler2.JobCount(); got != 1 {
		t.Errorf("JobCount after restart: got %d, want 1", got)
	}
}

func TestScheduler_DeleteRemovesJob(t *testing.T) {
	scheduler, _, instances, _, _ := setupScheduler(t)
	instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})
	scheduler.Register(context.Background(), &domain.BackupSchedule{
		ProjectID: "p1", Cron: "0 0 * * *", RetentionDays: 7, Enabled: true,
	})
	scheduler.Start(context.Background())
	t.Cleanup(scheduler.Stop)

	if scheduler.JobCount() != 1 {
		t.Errorf("expected 1 job before delete")
	}
	if err := scheduler.Unregister(context.Background(), "p1"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if scheduler.JobCount() != 0 {
		t.Errorf("expected 0 jobs after delete, got %d", scheduler.JobCount())
	}
}

func TestScheduler_NotLeader_DoesNotFire(t *testing.T) {
	// Leader lock that always refuses: scheduler must register the
	// schedule but never call adapter.TriggerManual.
	scheduler, _, instances, adapter, _ := setupScheduler(t)
	instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})
	scheduler.leadership = NewLeadership(&refusingLock{})

	scheduler.Register(context.Background(), &domain.BackupSchedule{
		ProjectID: "p1", Cron: "@every 200ms", RetentionDays: 7, Enabled: true,
	})
	scheduler.Start(context.Background())
	t.Cleanup(scheduler.Stop)

	time.Sleep(700 * time.Millisecond)
	adapter.muCount.Lock()
	defer adapter.muCount.Unlock()
	if adapter.triggerCalls != 0 {
		t.Errorf("non-leader still fired backups: %d", adapter.triggerCalls)
	}
}

// refusingLock always returns acquired=false.
type refusingLock struct{}

func (l *refusingLock) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return nil, false, nil
}
