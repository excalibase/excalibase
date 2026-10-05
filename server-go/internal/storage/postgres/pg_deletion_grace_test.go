//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A scheduled deletion's dates and backup choice survive a round trip, and a
// cancel can clear them again.
func TestInstances_DeletionScheduleRoundTrips(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-grace001", "org-grace")
	inst.Status = string(domain.StatusPaused)
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	scheduled := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	inst.Status = string(domain.StatusPendingDeletion)
	inst.DeletionDeleteBackups = true
	inst.DeletionScheduledAt = &scheduled
	due := scheduled.Add(7 * 24 * time.Hour)
	inst.DeletionDueAt = &due
	if err := store.UpdateIfStatus(inst, string(domain.StatusPaused)); err != nil {
		t.Fatalf("UpdateIfStatus: %v", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if got.Status != string(domain.StatusPendingDeletion) || !got.DeletionDeleteBackups ||
		got.DeletionDueAt == nil || !got.DeletionDueAt.Equal(*inst.DeletionDueAt) ||
		got.DeletionScheduledAt == nil {
		t.Fatalf("schedule lost: %+v", got)
	}

	got.Status = string(domain.StatusPaused)
	got.DeletionDeleteBackups = false
	got.DeletionScheduledAt, got.DeletionDueAt = nil, nil
	if err := store.UpdateIfStatus(got, string(domain.StatusPendingDeletion)); err != nil {
		t.Fatalf("cancel write: %v", err)
	}
	after, _ := store.FindByProjectID(inst.ProjectID)
	if after.DeletionDueAt != nil || after.DeletionDeleteBackups {
		t.Fatalf("cancel did not clear the schedule: %+v", after)
	}
}

// A project in its grace period gave its org slot up when it was deleted.
func TestInstances_PendingDeletionFreesTheOrgSlot(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-grace002", "org-grace2")
	inst.Status = string(domain.StatusPendingDeletion)
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n, err := store.CountOrgProjects("org-grace2"); err != nil || n != 0 {
		t.Fatalf("count = %d, %v; want 0", n, err)
	}
}

func TestRetainedBackups_DueAndDelete(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	due := storage.RetainedBackup{ProjectID: "proj-kept001", OrgID: "org-k", DeploymentMode: domain.ModeK8s,
		DeletedAt: now.Add(-15 * 24 * time.Hour), PurgeAfter: now.Add(-time.Hour)}
	later := storage.RetainedBackup{ProjectID: "proj-kept002", OrgID: "org-k", DeploymentMode: domain.ModeDocker,
		DeletedAt: now, PurgeAfter: now.Add(14 * 24 * time.Hour)}
	for _, r := range []storage.RetainedBackup{due, later} {
		if err := store.RecordRetainedBackups(r); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// Recording again replaces rather than failing.
	if err := store.RecordRetainedBackups(due); err != nil {
		t.Fatalf("re-record: %v", err)
	}

	got, err := store.DueRetainedBackups(now)
	if err != nil || len(got) != 1 || got[0].ProjectID != due.ProjectID || got[0].DeploymentMode != domain.ModeK8s {
		t.Fatalf("due = %+v, %v", got, err)
	}
	if err := store.DeleteRetainedBackups(due.ProjectID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := store.DueRetainedBackups(now.Add(30 * 24 * time.Hour)); len(got) != 1 || got[0].ProjectID != later.ProjectID {
		t.Fatalf("after delete = %+v", got)
	}
}
