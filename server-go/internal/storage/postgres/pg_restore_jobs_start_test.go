//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func runningJob(id, source, mode, kind, value string) *domain.RestoreJob {
	return &domain.RestoreJob{
		ID: id, SourceProjectID: source, NewProjectID: "dst-" + id, Mode: mode,
		Status: domain.RestoreStatusRunning, TargetKind: kind, TargetValue: value, Owner: "replica-a",
	}
}

func TestStartRestoreJob_RefusesTheSameRestoreWhileOneRuns(t *testing.T) {
	rs := NewRestoreJobs(testStore(t))
	ctx := context.Background()
	if err := rs.StartRestoreJob(ctx, runningJob("a", "src", domain.RestoreModeNewProject, "time", "T1")); err != nil {
		t.Fatalf("first start: %v", err)
	}
	err := rs.StartRestoreJob(ctx, runningJob("b", "src", domain.RestoreModeNewProject, "time", "T1"))
	if !errors.Is(err, storage.ErrRestoreAlreadyRunning) {
		t.Fatalf("a second restore of the same source and time must be refused, got %v", err)
	}
	if err := rs.StartRestoreJob(ctx, runningJob("c", "src", domain.RestoreModeNewProject, "time", "T2")); err != nil {
		t.Fatalf("a different restore point is a different restore: %v", err)
	}
	got, _ := rs.FindRestoreJob(ctx, "src", "a")
	if got == nil || got.Mode != domain.RestoreModeNewProject {
		t.Fatalf("mode must round-trip: %+v", got)
	}
}

func TestStartRestoreJob_InPlaceExcludesEveryOtherRestoreOfTheProject(t *testing.T) {
	rs := NewRestoreJobs(testStore(t))
	ctx := context.Background()
	if err := rs.StartRestoreJob(ctx, runningJob("copy", "src", domain.RestoreModeNewProject, "latest", "")); err != nil {
		t.Fatal(err)
	}
	err := rs.StartRestoreJob(ctx, runningJob("replace", "src", domain.RestoreModeInPlace, "time", "T1"))
	if !errors.Is(err, storage.ErrRestoreAlreadyRunning) {
		t.Fatalf("replacing a project while a copy of it is being made must wait, got %v", err)
	}
	finished := domain.RestoreJob{ID: "copy", Status: domain.RestoreStatusCompleted}
	if ok, err := rs.UpdateRunningRestoreJob(ctx, &finished, "replica-a"); err != nil || !ok {
		t.Fatalf("complete: ok=%v err=%v", ok, err)
	}
	if err := rs.StartRestoreJob(ctx, runningJob("replace", "src", domain.RestoreModeInPlace, "time", "T1")); err != nil {
		t.Fatalf("nothing else runs now: %v", err)
	}
	err = rs.StartRestoreJob(ctx, runningJob("copy2", "src", domain.RestoreModeNewProject, "time", "T9"))
	if !errors.Is(err, storage.ErrRestoreAlreadyRunning) {
		t.Fatalf("a copy while the project is being replaced must wait, got %v", err)
	}
	got, _ := rs.FindRestoreJob(ctx, "src", "replace")
	if got == nil || got.Mode != domain.RestoreModeInPlace {
		t.Fatalf("in-place job: %+v", got)
	}
}

// Two clicks arrive together: exactly one restore starts.
func TestStartRestoreJob_ConcurrentDuplicatesStartOnce(t *testing.T) {
	rs := NewRestoreJobs(testStore(t))
	ctx := context.Background()
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = rs.StartRestoreJob(ctx, runningJob("dup-"+string(rune('a'+i)), "src", domain.RestoreModeInPlace, "time", "T1"))
		}(i)
	}
	wg.Wait()
	started := 0
	for _, err := range errs {
		switch {
		case err == nil:
			started++
		case !errors.Is(err, storage.ErrRestoreAlreadyRunning):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if started != 1 {
		t.Fatalf("started %d restores, want 1", started)
	}
}

func TestFailAbandonedRestoreJobs_ReportsTheMode(t *testing.T) {
	rs := NewRestoreJobs(testStore(t))
	ctx := context.Background()
	if err := rs.StartRestoreJob(ctx, runningJob("gone", "src", domain.RestoreModeInPlace, "latest", "")); err != nil {
		t.Fatal(err)
	}
	failed, err := rs.FailAbandonedRestoreJobs(ctx, "replica-b", 0*time.Second, "abandoned")
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	if failed[0].Mode != domain.RestoreModeInPlace || failed[0].SourceProjectID != "src" {
		t.Fatalf("swept job must carry its mode and source: %+v", failed[0])
	}
}
