package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakePauser implements provisioner.Pauser for tests. Records calls.
type fakePauser struct {
	mu          sync.Mutex
	pauseCalls  int
	resumeCalls int
	pauseErr    error
	resumeErr   error
}

func (f *fakePauser) StopReplication(_ context.Context, _, _ string) error { return nil }

func (f *fakePauser) Pause(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseCalls++
	return f.pauseErr
}
func (f *fakePauser) Resume(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCalls++
	return f.resumeErr
}

// fakeBackupTrigger satisfies the BackupTrigger interface — counts
// calls + simulates success/failure.
type fakeBackupTrigger struct {
	calls int
	err   error
}

func (f *fakeBackupTrigger) BackupsConfigured(string) (bool, error) { return true, nil }

func (f *fakeBackupTrigger) BackupStatus(_ context.Context, _, _ string) (string, error) {
	return "COMPLETED", nil
}

func (f *fakeBackupTrigger) TriggerManualBackup(_ context.Context, _ string) (map[string]interface{}, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return map[string]interface{}{"id": "bk-1", "status": "IN_PROGRESS"}, nil
}

func setupPauseTest(t *testing.T) (*PauseService, *storage.FileSystemStore, *fakePauser, *fakeBackupTrigger) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	pauser := &fakePauser{}
	bk := &fakeBackupTrigger{}
	pausers := map[domain.DeploymentMode]provisioner.Pauser{
		domain.ModeK8s:    pauser,
		domain.ModeDocker: pauser,
	}
	svc := NewPauseService(PauseServiceConfig{
		Instances: store,
		Pausers:   pausers,
		Backups:   bk,
	})
	return svc, store, pauser, bk
}

func TestPauseService_Pause_HappyPath_BackupThenPause(t *testing.T) {
	svc, store, pauser, bk := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
	})

	if err := svc.Pause(context.Background(), "p1", domain.PauseReasonManual); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if bk.calls != 1 {
		t.Errorf("backup must run before pause: got %d calls", bk.calls)
	}
	if pauser.pauseCalls != 1 {
		t.Errorf("provisioner Pause: got %d, want 1", pauser.pauseCalls)
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != string(domain.StatusPaused) {
		t.Errorf("status: got %q, want PAUSED", got.Status)
	}
	if got.PauseReason != domain.PauseReasonManual {
		t.Errorf("PauseReason: got %q", got.PauseReason)
	}
}

func TestPauseService_Pause_BackupFails_StaysActive(t *testing.T) {
	svc, store, pauser, bk := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
	})
	bk.err = errors.New("R2 unreachable")

	err := svc.Pause(context.Background(), "p1", domain.PauseReasonIdle7Days)
	if err == nil {
		t.Fatal("expected error when backup fails")
	}
	if pauser.pauseCalls != 0 {
		t.Errorf("workload must NOT stop when backup fails: got %d Pause calls", pauser.pauseCalls)
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != "ACTIVE" {
		t.Errorf("project must remain ACTIVE on backup failure, got %q", got.Status)
	}
}

func TestPauseService_Pause_AlreadyPaused_NoOp(t *testing.T) {
	svc, store, pauser, bk := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
		PauseReason: domain.PauseReasonIdle7Days,
	})

	if err := svc.Pause(context.Background(), "p1", domain.PauseReasonManual); err != nil {
		t.Fatalf("Pause should be idempotent: %v", err)
	}
	if bk.calls != 0 {
		t.Errorf("no backup on already-paused: got %d", bk.calls)
	}
	if pauser.pauseCalls != 0 {
		t.Errorf("no provisioner call on already-paused: got %d", pauser.pauseCalls)
	}
}

func TestPauseService_Pause_ModeWithoutPauser_Refused(t *testing.T) {
	svc, store, _, _ := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "unwired-1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.DeploymentMode("unwired"),
	})

	err := svc.Pause(context.Background(), "unwired-1", domain.PauseReasonManual)
	if err == nil {
		t.Fatal("a mode with no pauser wired must refuse pause, not silently succeed")
	}
	if !errors.Is(err, ErrPauseUnsupported) {
		t.Errorf("expected ErrPauseUnsupported, got %v", err)
	}
}

func TestPauseService_Resume_HappyPath(t *testing.T) {
	svc, store, pauser, _ := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
		PauseReason:  domain.PauseReasonIdle7Days,
		LastActiveAt: &domain.FlexTime{Time: time.Now().Add(-9 * 24 * time.Hour)},
	})

	if err := svc.Resume(context.Background(), "p1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if pauser.resumeCalls != 1 {
		t.Errorf("provisioner Resume: got %d, want 1", pauser.resumeCalls)
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != "ACTIVE" {
		t.Errorf("status: got %q, want ACTIVE", got.Status)
	}
	if got.PauseReason != "" {
		t.Errorf("PauseReason should clear on resume, got %q", got.PauseReason)
	}
	if got.LastActiveAt == nil || time.Since(got.LastActiveAt.Time) > 5*time.Second {
		t.Errorf("LastActiveAt must reset to ~now on resume, got %v", got.LastActiveAt)
	}
}

func TestPauseService_Resume_NotPaused_NoOp(t *testing.T) {
	svc, store, pauser, _ := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker,
	})
	if err := svc.Resume(context.Background(), "p1"); err != nil {
		t.Fatalf("Resume on ACTIVE should be idempotent: %v", err)
	}
	if pauser.resumeCalls != 0 {
		t.Errorf("no provisioner call when already ACTIVE: got %d", pauser.resumeCalls)
	}
}

func TestPauseService_Resume_NotFound(t *testing.T) {
	svc, _, _, _ := setupPauseTest(t)
	err := svc.Resume(context.Background(), "missing")
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestPauseService_Pause_WorkloadStopFails_RetainsPausing(t *testing.T) {
	// Backup succeeded but provisioner.Pause failed. We don't roll
	// back the backup (nothing to undo) but we DO leave the project
	// in PAUSING so an operator sees a stuck state and can intervene.
	svc, store, pauser, _ := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
	})
	pauser.pauseErr = errors.New("docker daemon refused")

	if err := svc.Pause(context.Background(), "p1", domain.PauseReasonManual); err == nil {
		t.Fatal("expected error when provisioner Pause fails")
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != string(domain.StatusPausing) {
		t.Errorf("expected stuck PAUSING status, got %q", got.Status)
	}
}
