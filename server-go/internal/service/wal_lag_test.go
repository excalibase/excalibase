package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestWalLag_DockerAdapterDerivesFromRecords(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	records := &fakeBackupRecordStore{}
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Records: records,
	})
	store.Save(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})

	// Two completed records — wal-lag should reflect the most recent.
	now := time.Now().UTC()
	records.Save(context.Background(), &domain.BackupRecord{
		ID: "old", ProjectID: "p1", Status: "COMPLETED",
		Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339),
	})
	records.Save(context.Background(), &domain.BackupRecord{
		ID: "recent", ProjectID: "p1", Status: "COMPLETED",
		Timestamp: now.Add(-30 * time.Second).Format(time.RFC3339),
	})

	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeDocker: adapter,
	}, dir)

	info, err := svc.GetWalLag(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetWalLag: %v", err)
	}
	if info.LastSuccess == "" {
		t.Errorf("LastSuccess empty")
	}
	if info.SecondsSinceLastSuccess < 0 || info.SecondsSinceLastSuccess > 120 {
		t.Errorf("SecondsSinceLastSuccess: got %d", info.SecondsSinceLastSuccess)
	}
}

func TestWalLag_NoCompletedRecords_NegativeOne(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Records: &fakeBackupRecordStore{},
	})
	store.Save(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeDocker, Status: "ACTIVE",
	})
	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeDocker: adapter,
	}, dir)

	info, err := svc.GetWalLag(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetWalLag: %v", err)
	}
	if info.SecondsSinceLastSuccess != -1 {
		t.Errorf("SecondsSinceLastSuccess: got %d, want -1", info.SecondsSinceLastSuccess)
	}
}

func TestWalLag_K8sAdapterReturnsUnsupported(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	store.Save(&domain.DatabaseInstance{
		ProjectID: "p1", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	})
	// K8s adapter doesn't implement WalLagAdvertiser.
	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeK8s: &fakeAdapter{},
	}, dir)

	_, err := svc.GetWalLag(context.Background(), "p1")
	if !errors.Is(err, ErrWalLagUnsupported) {
		t.Errorf("expected ErrWalLagUnsupported, got %v", err)
	}
}
