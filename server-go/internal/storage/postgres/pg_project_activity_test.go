//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const activityProject = "act-1"

func saveActivityProject(t *testing.T, store *Store, projectID string) {
	t.Helper()
	if err := store.Save(&domain.DatabaseInstance{ProjectID: projectID, OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestProjectActivity_TouchInsertsThenUpdates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveActivityProject(t, store, activityProject)

	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if err := store.TouchProjectActivity(ctx, activityProject, "api", first); err != nil {
		t.Fatalf("first touch: %v", err)
	}
	got, ok, err := store.GetProjectActivity(ctx, activityProject)
	if err != nil || !ok {
		t.Fatalf("GetProjectActivity: ok=%v err=%v", ok, err)
	}
	if !got.LastSeenAt.Equal(first) || got.LastSeenSource != "api" {
		t.Errorf("first row: %+v", got)
	}

	second := first.Add(2 * time.Hour)
	if err := store.TouchProjectActivity(ctx, activityProject, "policy_fetch", second); err != nil {
		t.Fatalf("second touch: %v", err)
	}
	got, _, _ = store.GetProjectActivity(ctx, activityProject)
	if !got.LastSeenAt.Equal(second) || got.LastSeenSource != "policy_fetch" {
		t.Errorf("upsert did not replace row: %+v", got)
	}
}

func TestProjectActivity_GetMissingReturnsNotFound(t *testing.T) {
	store := testStore(t)
	_, ok, err := store.GetProjectActivity(context.Background(), "never-seen")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false for a project with no activity row")
	}
}

func TestProjectActivity_TouchUnknownProjectIsRejected(t *testing.T) {
	store := testStore(t)
	if err := store.TouchProjectActivity(context.Background(), "ghost", "api", time.Now()); err == nil {
		t.Error("touching a project that does not exist must fail (FK guard against junk rows)")
	}
}

func TestProjectActivity_ListAndCascadeDelete(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveActivityProject(t, store, "act-a")
	saveActivityProject(t, store, "act-b")
	now := time.Now().UTC().Truncate(time.Microsecond)
	_ = store.TouchProjectActivity(ctx, "act-a", "api", now)
	_ = store.TouchProjectActivity(ctx, "act-b", "functions", now)

	all, err := store.ListProjectActivity(ctx)
	if err != nil {
		t.Fatalf("ListProjectActivity: %v", err)
	}
	if len(all) != 2 || all["act-b"].LastSeenSource != "functions" {
		t.Errorf("list: %+v", all)
	}

	if err := store.Delete("act-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.GetProjectActivity(ctx, "act-a"); ok {
		t.Error("activity row must cascade-delete with its project")
	}
}
