package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testSnapDB = "snap-db"

func setupSnapshotTest(t *testing.T) *SnapshotService {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.ExecOutput["org-snap-db/snap-db-postgres-1"] = "-- pg_dump output\nCREATE TABLE..."
	store.Create(&domain.DatabaseInstance{
		ProjectID: testSnapDB, Namespace: "org-snap-db", Status: "ACTIVE",
	})
	return NewSnapshotService(store, mock, dir)
}

func TestExportSnapshot(t *testing.T) {
	svc := setupSnapshotTest(t)
	info, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if info.ProjectID != testSnapDB {
		t.Errorf("projectId: got %s", info.ProjectID)
	}
	if info.Size == 0 {
		t.Error("size should be > 0")
	}
}

func TestListSnapshots(t *testing.T) {
	svc := setupSnapshotTest(t)
	svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{})
	list, _ := svc.ListSnapshots(testSnapDB)
	if len(list) != 1 {
		t.Errorf("expected 1 snapshot, got %d", len(list))
	}
}

func TestDownloadSnapshotNotFound(t *testing.T) {
	svc := setupSnapshotTest(t)
	_, _, err := svc.DownloadSnapshot(testSnapDB, "nonexistent")
	if err == nil {
		t.Error("expected error for missing snapshot")
	}
}

func TestDeleteSnapshot(t *testing.T) {
	svc := setupSnapshotTest(t)
	info, _ := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{})
	svc.DeleteSnapshot(testSnapDB, info.ID)
	list, _ := svc.ListSnapshots(testSnapDB)
	if len(list) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(list))
	}
}
