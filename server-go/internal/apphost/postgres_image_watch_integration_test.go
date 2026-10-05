//go:build integration

package apphost_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// EXC-542: the watcher reads every opted-in app across projects and records
// what it saw without moving the version a developer's edit is checked against.
func TestPGAppStore_ImageWatch(t *testing.T) {
	store := newPGAppStore(t)
	watched := sampleApp("proj_watch_a", "app_watch_a", "web")
	watched.AutoDeploy = true
	other := sampleApp("proj_watch_b", "app_watch_b", "web")
	other.AutoDeploy = true
	unwatched := sampleApp("proj_watch_a", "app_watch_c", "worker")
	for _, app := range []*apphost.App{watched, other, unwatched} {
		if err := store.Create(app, 5); err != nil {
			t.Fatalf("create %s: %v", app.ID, err)
		}
	}

	listed, err := store.ListAutoDeploy()
	if err != nil {
		t.Fatalf("ListAutoDeploy: %v", err)
	}
	seen := map[string]bool{}
	for _, app := range listed {
		seen[app.ID] = true
	}
	if !seen[watched.ID] || !seen[other.ID] || seen[unwatched.ID] {
		t.Fatalf("listed %v; want both opted-in apps and not the other", seen)
	}

	checked := time.Now().UTC().Truncate(time.Millisecond)
	watch := apphost.ImageWatch{Digest: "sha256:" + strings.Repeat("aa", 32), CheckedAt: checked, Error: "the registry did not answer"}
	if err := store.RecordImageWatch(watched.ProjectID, watched.ID, watch); err != nil {
		t.Fatalf("RecordImageWatch: %v", err)
	}
	got, err := store.Get(watched.ProjectID, watched.ID)
	if err != nil || got.ImageWatch == nil {
		t.Fatalf("get: %+v %v", got, err)
	}
	if got.ImageWatch.Digest != watch.Digest || got.ImageWatch.Error != watch.Error || !got.ImageWatch.CheckedAt.Equal(checked) {
		t.Fatalf("watch = %+v, want %+v", got.ImageWatch, watch)
	}
	if got.Version != watched.Version {
		t.Fatalf("a watch is observed state and must not move the version: %d -> %d", watched.Version, got.Version)
	}
	if err := store.RecordImageWatch("proj_watch_a", "app_missing", watch); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("an unknown app: %v", err)
	}
}
