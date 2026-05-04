package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestBackupRecords_SaveAndListByProject(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC3339)
	if err := br.Save(ctx, &domain.BackupRecord{
		ID: "b1", ProjectID: "p1", Timestamp: now, Type: "MANUAL", Status: "IN_PROGRESS",
	}); err != nil {
		t.Fatalf("Save b1: %v", err)
	}
	if err := br.Save(ctx, &domain.BackupRecord{
		ID: "b2", ProjectID: "p1", Timestamp: now, Type: "SCHEDULED", Status: "COMPLETED",
	}); err != nil {
		t.Fatalf("Save b2: %v", err)
	}
	// Different project — must not appear in p1's list.
	if err := br.Save(ctx, &domain.BackupRecord{
		ID: "other", ProjectID: "p2", Timestamp: now, Type: "MANUAL", Status: "COMPLETED",
	}); err != nil {
		t.Fatalf("Save other: %v", err)
	}

	got, err := br.ListByProject(ctx, "p1")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records, got %d", len(got))
	}
	for _, r := range got {
		if r.ProjectID != "p1" {
			t.Errorf("IDOR: leaked record from %q", r.ProjectID)
		}
	}
}

func TestBackupRecords_SaveUpserts(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	br.Save(ctx, &domain.BackupRecord{ID: "u1", ProjectID: "p", Timestamp: now, Type: "MANUAL", Status: "IN_PROGRESS"})
	br.Save(ctx, &domain.BackupRecord{ID: "u1", ProjectID: "p", Timestamp: now, Type: "MANUAL", Status: "COMPLETED"})

	got, _ := br.ListByProject(ctx, "p")
	if len(got) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(got))
	}
	if got[0].Status != "COMPLETED" {
		t.Errorf("status: got %q, want COMPLETED", got[0].Status)
	}
}

func TestBackupRecords_UpdateStatus(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	br.Save(ctx, &domain.BackupRecord{ID: "s1", ProjectID: "p", Timestamp: now, Type: "MANUAL", Status: "IN_PROGRESS"})
	if err := br.UpdateStatus(ctx, "s1", "FAILED"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := br.ListByProject(ctx, "p")
	if got[0].Status != "FAILED" {
		t.Errorf("status: got %q, want FAILED", got[0].Status)
	}

	if err := br.UpdateStatus(ctx, "missing", "COMPLETED"); err == nil {
		t.Error("expected error updating missing record")
	}
}
