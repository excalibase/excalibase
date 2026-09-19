//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRestoreJobs_RoundTrip(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j1", SourceProjectID: "src", NewProjectID: "dst",
		Status: domain.RestoreStatusRunning, CurrentStep: "fetch", TargetKind: "time", TargetValue: "2026-05-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := rs.FindRestoreJob(ctx, "src", "j1")
	if err != nil || got == nil {
		t.Fatalf("Find: %v", err)
	}
	if got.SourceProjectID != "src" || got.CurrentStep != "fetch" || got.TargetValue != "2026-05-01T00:00:00Z" {
		t.Errorf("roundtrip: %+v", got)
	}
}

func TestRestoreJobs_ListRunning(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r1", SourceProjectID: "s", NewProjectID: "d", Status: domain.RestoreStatusRunning, TargetKind: "latest"})
	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r2", SourceProjectID: "s", NewProjectID: "d2", Status: domain.RestoreStatusCompleted, TargetKind: "latest"})

	got, _ := rs.ListRunningRestoreJobs(ctx)
	if len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("running: %+v", got)
	}
}
