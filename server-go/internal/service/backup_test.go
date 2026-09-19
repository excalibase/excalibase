package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func setupBackupTest(t *testing.T) (*BackupService, *storage.FileSystemStore) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))
	svc.SetProjectRegistrar(&fakeRegistrar{store: store})
	armRestore(svc, mock, store)

	store.Create(&domain.DatabaseInstance{
		ProjectID: "bk-db", OrgID: "org", Namespace: "org-bk-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL,
	})
	return svc, store
}

func TestTriggerManualBackup(t *testing.T) {
	svc, _ := setupBackupTest(t)
	result, err := svc.TriggerManualBackup(context.Background(), "bk-db")
	if err != nil {
		t.Fatalf("TriggerManualBackup: %v", err)
	}
	if result["status"] != "IN_PROGRESS" {
		t.Errorf("status: got %v", result["status"])
	}
	if result["projectId"] != "bk-db" {
		t.Errorf("projectId: got %v", result["projectId"])
	}
}

func TestTriggerBackupNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewBackupService(store, k8s.NewMockClient(), dir, StaticBackupStorage(r2Storage()))

	_, err := svc.TriggerManualBackup(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestListBackupsEmpty(t *testing.T) {
	svc, _ := setupBackupTest(t)
	list, _ := svc.ListBackups("bk-db")
	if len(list) != 0 {
		t.Errorf("expected 0 backups, got %d", len(list))
	}
}

func TestListBackupsAfterTrigger(t *testing.T) {
	svc, _ := setupBackupTest(t)
	svc.TriggerManualBackup(context.Background(), "bk-db")

	list, _ := svc.ListBackups("bk-db")
	if len(list) != 1 {
		t.Errorf("expected 1 backup, got %d", len(list))
	}
}

func TestRestoreFromBackup(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))
	svc.SetProjectRegistrar(&fakeRegistrar{store: store})
	armRestore(svc, mock, store)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "bk-db", OrgID: "org", Namespace: "org-bk-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL,
	})

	resp, err := svc.RestoreFromBackup(context.Background(), "bk-db", domain.RestoreRequest{
		NewProjectName: "bk-db-restored", TargetProjectID: "bk-db-restored",
	})
	if err != nil {
		t.Fatalf("RestoreFromBackup: %v", err)
	}
	if resp.ProjectID != "bk-db-restored" {
		t.Errorf("projectId: got %s", resp.ProjectID)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s", resp.Status)
	}

	// Verify namespace was created
	if !mock.Namespaces["org-bk-db-restored"] {
		t.Error("restore namespace not created")
	}

	// Verify S3 secret was created
	if _, ok := mock.Secrets["org-bk-db-restored/backup-s3-creds"]; !ok {
		t.Error("backup S3 credentials not created in restore namespace")
	}

	// Verify restore CRD was applied
	if _, ok := mock.CRDs["org-bk-db-restored/bk-db-restored-postgres"]; !ok {
		t.Error("restore Cluster CRD not applied")
	}
}

func TestRestoreFromBackupNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewBackupService(store, k8s.NewMockClient(), dir, StaticBackupStorage(r2Storage()))

	_, err := svc.RestoreFromBackup(context.Background(), "nope", domain.RestoreRequest{
		NewProjectName: "restored", TargetProjectID: "restored",
	})
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestRestoreFromBackupPITR(t *testing.T) {
	svc, _ := setupBackupTest(t)
	targetTime := &domain.FlexTime{Time: time.Now().Add(-1 * time.Hour)}

	resp, err := svc.RestoreFromBackup(context.Background(), "bk-db", domain.RestoreRequest{
		NewProjectName: "bk-db-pitr", TargetProjectID: "bk-db-pitr",
		TargetTime: targetTime,
	})
	if err != nil {
		t.Fatalf("RestoreFromBackup PITR: %v", err)
	}
	if resp.ProjectID != "bk-db-pitr" {
		t.Errorf("projectId: got %s", resp.ProjectID)
	}
}
