package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeAdapter is a test BackupAdapter that records calls. Access to
// counters is mutex-guarded so the scheduler tests can race against
// the cron goroutine without a data race.
type fakeAdapter struct {
	muCount        sync.Mutex
	configureCalls int
	triggerCalls   int
	listCalls      int
	restoreCalls   int
	configureErr   error
	triggerErr     error
	listErr        error
	restoreErr     error
	listResult     []BackupRef
}

func (f *fakeAdapter) Configure(_ context.Context, _ *domain.DatabaseInstance, _ string, _ int) error {
	f.muCount.Lock()
	f.configureCalls++
	f.muCount.Unlock()
	return f.configureErr
}

func (f *fakeAdapter) BackupsConfigured() bool { return true }

func (f *fakeAdapter) TriggerManual(_ context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	f.muCount.Lock()
	f.triggerCalls++
	f.muCount.Unlock()
	if f.triggerErr != nil {
		return BackupRef{}, f.triggerErr
	}
	return BackupRef{ID: "bk-1", ProjectID: inst.ProjectID, Status: "IN_PROGRESS", Type: "MANUAL"}, nil
}

func (f *fakeAdapter) List(_ context.Context, _ *domain.DatabaseInstance) ([]BackupRef, error) {
	f.muCount.Lock()
	f.listCalls++
	f.muCount.Unlock()
	return f.listResult, f.listErr
}

func (f *fakeAdapter) Restore(_ context.Context, _ *domain.DatabaseInstance, _ domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	f.muCount.Lock()
	f.restoreCalls++
	f.muCount.Unlock()
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	return &domain.ProvisioningResponse{ProjectID: "restored", Status: "RESTORING"}, nil
}

func setupAdapterTest(t *testing.T) (*BackupService, *storage.FileSystemStore, *fakeAdapter, *fakeAdapter) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	k8sFake := &fakeAdapter{}
	dockerFake := &fakeAdapter{}
	adapters := map[domain.DeploymentMode]BackupAdapter{
		domain.ModeK8s:    k8sFake,
		domain.ModeDocker: dockerFake,
	}
	svc := NewBackupServiceWithAdapters(store, adapters, dir)
	return svc, store, k8sFake, dockerFake
}

func TestBackupAdapter_DispatchesK8sToK8sAdapter(t *testing.T) {
	svc, store, k8sFake, dockerFake := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "k8s-1", OrgID: "org", Namespace: "org-k8s-1",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	})

	if _, err := svc.TriggerManualBackup(context.Background(), "k8s-1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if k8sFake.triggerCalls != 1 {
		t.Errorf("k8s trigger calls: got %d, want 1", k8sFake.triggerCalls)
	}
	if dockerFake.triggerCalls != 0 {
		t.Errorf("docker should not have been called: %d", dockerFake.triggerCalls)
	}
}

func TestBackupAdapter_DispatchesDockerToDockerAdapter(t *testing.T) {
	svc, store, k8sFake, dockerFake := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "dk-1", OrgID: "org", Namespace: "org-dk-1",
		DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})

	if _, err := svc.TriggerManualBackup(context.Background(), "dk-1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if dockerFake.triggerCalls != 1 {
		t.Errorf("docker trigger calls: got %d, want 1", dockerFake.triggerCalls)
	}
	if k8sFake.triggerCalls != 0 {
		t.Errorf("k8s should not have been called: %d", k8sFake.triggerCalls)
	}
}

func TestBackupAdapter_EmptyModeFallsBackToK8s(t *testing.T) {
	// Pre-Phase-0 instances may exist with DeploymentMode="" before
	// the migration backfilled them. Dispatch must treat empty as k8s.
	svc, store, k8sFake, _ := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "legacy", OrgID: "org", Namespace: "org-legacy",
		Status: "ACTIVE", // DeploymentMode intentionally unset
	})

	if _, err := svc.TriggerManualBackup(context.Background(), "legacy"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if k8sFake.triggerCalls != 1 {
		t.Errorf("legacy must dispatch to k8s: got %d", k8sFake.triggerCalls)
	}
}

func TestBackupAdapter_UnsupportedModeReturnsError(t *testing.T) {
	svc, store, _, _ := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "unwired-1", OrgID: "org",
		DeploymentMode: domain.DeploymentMode("unwired"), Status: "ACTIVE",
	})

	_, err := svc.TriggerManualBackup(context.Background(), "unwired-1")
	if err == nil {
		t.Fatal("expected error for unsupported mode")
	}
	if !errors.Is(err, ErrUnsupportedBackupMode) {
		t.Errorf("error: got %v, want ErrUnsupportedBackupMode", err)
	}
}

func TestBackupAdapter_InstanceNotFound(t *testing.T) {
	svc, _, _, _ := setupAdapterTest(t)
	_, err := svc.TriggerManualBackup(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestBackupAdapter_PropagatesAdapterError(t *testing.T) {
	svc, store, k8sFake, _ := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "err-1", OrgID: "org",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	})
	k8sFake.triggerErr = errors.New("adapter failed")

	_, err := svc.TriggerManualBackup(context.Background(), "err-1")
	if err == nil || err.Error() != "adapter failed" {
		t.Errorf("expected propagated error, got %v", err)
	}
}

func TestBackupAdapter_ListDispatches(t *testing.T) {
	svc, store, k8sFake, dockerFake := setupAdapterTest(t)
	dockerFake.listResult = []BackupRef{{ID: "d1", ProjectID: "dk", Status: "COMPLETED"}}
	store.Create(&domain.DatabaseInstance{
		ProjectID: "dk", OrgID: "org",
		DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})

	got, err := svc.ListBackups("dk")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if k8sFake.listCalls != 0 {
		t.Errorf("k8s adapter should not have been called")
	}
	if dockerFake.listCalls != 1 {
		t.Errorf("docker list calls: got %d, want 1", dockerFake.listCalls)
	}
}

func TestBackupAdapter_RestoreDispatches(t *testing.T) {
	svc, store, _, dockerFake := setupAdapterTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "src", OrgID: "org",
		DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})

	resp, err := svc.RestoreFromBackup(context.Background(), "src", domain.RestoreRequest{
		NewProjectName: "dst", TargetProjectID: "dst",
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if dockerFake.restoreCalls != 1 {
		t.Errorf("docker restore calls: got %d, want 1", dockerFake.restoreCalls)
	}
	if resp.Status != "RESTORING" {
		t.Errorf("status: got %s", resp.Status)
	}
}
