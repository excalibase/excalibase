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

// The restore target id is generated (EXC-415), so the job row is the only
// place the new project's admin can learn it. Project-scoped lookup (EXC-399)
// must still resolve for BOTH named projects and for nobody else.
func TestRestoreJobs_GeneratedTargetIDStaysProjectScoped(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	const generatedTarget = "proj-gen4t9xq2p"
	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j-scoped", SourceProjectID: "proj-source01", NewProjectID: generatedTarget,
		NewProjectName: "restored orders",
		Status:         domain.RestoreStatusRunning, TargetKind: "latest",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for _, caller := range []string{"proj-source01", generatedTarget} {
		got, err := rs.FindRestoreJob(ctx, caller, "j-scoped")
		if err != nil || got == nil {
			t.Fatalf("caller %q must see the job: %v", caller, err)
		}
		if got.NewProjectID != generatedTarget || got.NewProjectName != "restored orders" {
			t.Errorf("caller %q got: %+v", caller, got)
		}
	}

	got, err := rs.FindRestoreJob(ctx, "proj-stranger1", "j-scoped")
	if err != nil {
		t.Fatalf("Find as a third project: %v", err)
	}
	if got != nil {
		t.Errorf("a project named nowhere on the job must not reach it: %+v", got)
	}
}
