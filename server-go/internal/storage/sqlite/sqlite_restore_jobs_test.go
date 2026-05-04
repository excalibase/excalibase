package sqlite

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

	got, err := rs.FindRestoreJob(ctx, "j1")
	if err != nil || got == nil {
		t.Fatalf("FindRestoreJob: %v", err)
	}
	if got.SourceProjectID != "src" || got.NewProjectID != "dst" || got.CurrentStep != "fetch" {
		t.Errorf("roundtrip: %+v", got)
	}
	if got.TargetKind != "time" || got.TargetValue != "2026-05-01T00:00:00Z" {
		t.Errorf("target: %+v", got)
	}
}

func TestRestoreJobs_UpsertOverwrites(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "u", SourceProjectID: "s", NewProjectID: "d", Status: domain.RestoreStatusRunning, CurrentStep: "init", TargetKind: "latest"})
	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "u", SourceProjectID: "s", NewProjectID: "d", Status: domain.RestoreStatusCompleted, CurrentStep: "", TargetKind: "latest"})

	got, _ := rs.FindRestoreJob(ctx, "u")
	if got.Status != domain.RestoreStatusCompleted {
		t.Errorf("status: got %q", got.Status)
	}
}

func TestRestoreJobs_ListRunning_FiltersStatus(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r1", SourceProjectID: "s", NewProjectID: "d1", Status: domain.RestoreStatusRunning, TargetKind: "latest"})
	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r2", SourceProjectID: "s", NewProjectID: "d2", Status: domain.RestoreStatusCompleted, TargetKind: "latest"})
	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r3", SourceProjectID: "s", NewProjectID: "d3", Status: domain.RestoreStatusFailed, TargetKind: "latest"})

	got, err := rs.ListRunningRestoreJobs(ctx)
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("running: %+v", got)
	}
}

func TestRestoreJobs_FindNotFound(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	got, err := rs.FindRestoreJob(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing job")
	}
}
