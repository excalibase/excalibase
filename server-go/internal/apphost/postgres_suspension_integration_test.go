//go:build integration

package apphost_test

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// EXC-839: the stop is observed state. It is listed for the guard, survives a
// developer's edit, and clearing it never moves the version.
func TestPGAppStore_Suspension(t *testing.T) {
	store := newPGAppStore(t)
	app := sampleApp("proj_susp_a", "app_susp_a", "web")
	if err := store.Create(app, 5); err != nil {
		t.Fatal(err)
	}
	suspension := &apphost.Suspension{Kind: apphost.SuspensionCrashLoop, Reason: "kept crashing (10 restarts in the last hour)",
		Restarts: 10, WindowMinutes: 60, At: time.Now().UTC().Truncate(time.Millisecond)}
	if err := store.RecordSuspension(app.ProjectID, app.ID, suspension); err != nil {
		t.Fatalf("RecordSuspension: %v", err)
	}
	got, _ := store.Get(app.ProjectID, app.ID)
	if got.Suspension == nil || got.Suspension.Restarts != 10 || got.Suspension.Reason != suspension.Reason || !got.Suspension.At.Equal(suspension.At) {
		t.Fatalf("suspension = %+v", got.Suspension)
	}
	if got.Version != app.Version {
		t.Fatalf("version moved %d -> %d", app.Version, got.Version)
	}

	edit := *got
	edit.Suspension = nil
	edit.Port = 9090
	if err := store.Update(&edit, got.Version); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.Get(app.ProjectID, app.ID); after.Suspension == nil || after.Port != 9090 {
		t.Fatalf("an edit lost the suspension or was not applied: %+v", after)
	}

	if err := store.RecordSuspension(app.ProjectID, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.Get(app.ProjectID, app.ID); after.Suspension != nil {
		t.Fatalf("cleared suspension remains: %+v", after.Suspension)
	}
	if err := store.RecordSuspension(app.ProjectID, "app_missing", nil); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("unknown app: %v", err)
	}
}

func TestPGAppStore_ListSuspendableListsRunningAndFailedAppsOnly(t *testing.T) {
	store := newPGAppStore(t)
	running := sampleApp("proj_susp_b", "app_susp_run", "web")
	created := sampleApp("proj_susp_b", "app_susp_new", "api")
	for _, app := range []*apphost.App{running, created} {
		if err := store.Create(app, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Transition(running.ProjectID, running.ID, []string{running.Status}, apphost.StatusRunning); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListSuspendable()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, app := range listed {
		seen[app.ID] = true
	}
	if !seen[running.ID] || seen[created.ID] {
		t.Fatalf("listed %v, want the running app and not the one never deployed", seen)
	}
}
