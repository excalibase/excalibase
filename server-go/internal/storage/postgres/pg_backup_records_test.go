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

// A record's time reads back as RFC 3339, the format it was written in.
func TestBackupRecords_TimestampReadsBackAsRFC3339(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	if err := br.Save(ctx, &domain.BackupRecord{
		ID: "ts1", ProjectID: "pts", Timestamp: "2026-10-11T12:00:05Z", Type: "MANUAL", Status: "IN_PROGRESS",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := br.ListByProject(ctx, "pts")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByProject: %v %+v", err, got)
	}
	when, err := time.Parse(time.RFC3339, got[0].Timestamp)
	if err != nil || !when.Equal(time.Date(2026, 10, 11, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("timestamp %q: %v", got[0].Timestamp, err)
	}
}

// EXC-586: records go with the backups they describe, by project or up to a
// time, and never another project's.
func TestBackupRecords_DeleteByProjectAndThrough(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	for _, r := range []domain.BackupRecord{
		{ID: "d1", ProjectID: "pd", Timestamp: "2026-10-01T00:00:00Z", Type: "MANUAL"},
		{ID: "d2", ProjectID: "pd", Timestamp: "2026-10-05T00:00:00Z", Type: "MANUAL"},
		{ID: "d3", ProjectID: "pd", Timestamp: "2026-10-09T00:00:00Z", Type: "MANUAL"},
		{ID: "o1", ProjectID: "pd-other", Timestamp: "2026-10-01T00:00:00Z", Type: "MANUAL"},
	} {
		r := r
		if err := br.Save(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := br.DeleteThrough(ctx, "pd", time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("DeleteThrough: %v", err)
	}
	if got, _ := br.ListByProject(ctx, "pd"); len(got) != 1 || got[0].ID != "d3" {
		t.Errorf("after DeleteThrough: %+v", got)
	}
	if err := br.DeleteByProject(ctx, "pd"); err != nil {
		t.Fatalf("DeleteByProject: %v", err)
	}
	if got, _ := br.ListByProject(ctx, "pd"); len(got) != 0 {
		t.Errorf("after DeleteByProject: %+v", got)
	}
	if got, _ := br.ListByProject(ctx, "pd-other"); len(got) != 1 {
		t.Errorf("another project's records went: %+v", got)
	}
}
