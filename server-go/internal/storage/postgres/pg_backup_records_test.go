//go:build integration

package postgres

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
	cases := []domain.BackupRecord{
		{ID: "b1", ProjectID: "p1", Timestamp: now, Type: "MANUAL", Status: "IN_PROGRESS"},
		{ID: "b2", ProjectID: "p1", Timestamp: now, Type: "SCHEDULED", Status: "COMPLETED"},
		{ID: "other", ProjectID: "p2", Timestamp: now, Type: "MANUAL", Status: "COMPLETED"},
	}
	for _, c := range cases {
		c := c
		if err := br.Save(ctx, &c); err != nil {
			t.Fatalf("Save %s: %v", c.ID, err)
		}
	}

	got, err := br.ListByProject(ctx, "p1")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records for p1, got %d", len(got))
	}
	for _, r := range got {
		if r.ProjectID != "p1" {
			t.Errorf("IDOR: leaked %q", r.ProjectID)
		}
	}
}

func TestBackupRecords_UpdateStatus(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if err := br.Save(ctx, &domain.BackupRecord{
		ID: "u1", ProjectID: "p", Timestamp: now, Type: "MANUAL", Status: "IN_PROGRESS",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := br.UpdateStatus(ctx, "u1", "COMPLETED"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := br.ListByProject(ctx, "p")
	if got[0].Status != "COMPLETED" {
		t.Errorf("status: got %q", got[0].Status)
	}
}
