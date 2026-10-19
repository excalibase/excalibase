//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	slotOrg    = "org-slot"
	slotSource = "proj-slot0src"
)

// copyJob is a restore of slotSource into a new project, not yet registered.
func copyJob(id, value string) *domain.RestoreJob {
	job := runningJob(id, slotSource, domain.RestoreModeNewProject, "time", value)
	job.NewProjectID = "proj-slot0" + id
	return job
}

func slotStore(t *testing.T) (*Store, *RestoreJobsStore) {
	t.Helper()
	store := testStore(t)
	if err := store.Create(instanceRow(slotSource, slotOrg)); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	return store, NewRestoreJobs(store)
}

// EXC-639: a restore into a new project takes its organisation's slot when it
// is submitted, not when it registers the copy minutes later. While it runs,
// nothing else can take that slot, and the restore itself still can.
func TestRestoreIntoNewProject_HoldsTheSlotFromSubmission(t *testing.T) {
	store, jobs := slotStore(t)
	ctx := context.Background()
	slot := storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2}

	job := copyJob("a", "T1")
	if err := jobs.StartRestoreJob(ctx, job, slot); err != nil {
		t.Fatalf("start the restore: %v", err)
	}

	if held, err := store.CountOrgProjects(slotOrg); err != nil || held != 2 {
		t.Fatalf("held slots = %d (%v), want the source and the running restore", held, err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-slot0new", slotOrg), 2); !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("a new project while the restore holds the last slot = %v, want the limit", err)
	}
	if err := jobs.StartRestoreJob(ctx, copyJob("b", "T2"), slot); !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("a second copy while the first holds the last slot = %v, want the limit", err)
	}

	target := instanceRow(job.NewProjectID, slotOrg)
	target.Status = string(domain.StatusRestoring)
	if err := store.CreateWithinOrgLimit(target, 2); err != nil {
		t.Fatalf("the restore must register its copy in the slot it holds: %v", err)
	}
	if held, _ := store.CountOrgProjects(slotOrg); held != 2 {
		t.Fatalf("held slots after registration = %d, want 2: a registered copy is not counted twice", held)
	}
}

func TestRestoreIntoNewProject_FinishedRestoreReleasesItsHold(t *testing.T) {
	store, jobs := slotStore(t)
	ctx := context.Background()
	slot := storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2}
	if err := jobs.StartRestoreJob(ctx, copyJob("a", "T1"), slot); err != nil {
		t.Fatal(err)
	}

	failed := domain.RestoreJob{ID: "a", Status: domain.RestoreStatusFailed, FailureReason: "x"}
	if ok, err := jobs.UpdateRunningRestoreJob(ctx, &failed, "replica-a"); err != nil || !ok {
		t.Fatalf("fail the job: ok=%v err=%v", ok, err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-slot0new", slotOrg), 2); err != nil {
		t.Fatalf("a failed restore must give its slot back: %v", err)
	}
}

func TestRestoreIntoNewProject_RefusedAtSubmissionWhenTheOrgIsFull(t *testing.T) {
	store, jobs := slotStore(t)
	ctx := context.Background()
	if err := store.Create(instanceRow("proj-slot0two", slotOrg)); err != nil {
		t.Fatal(err)
	}

	err := jobs.StartRestoreJob(ctx, copyJob("a", "T1"), storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2})
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("StartRestoreJob = %v, want the limit", err)
	}
	if got, _ := jobs.FindRestoreJob(ctx, slotSource, "a"); got != nil {
		t.Fatal("a refused restore must leave no job behind")
	}
	// Replacing a project in place takes no slot.
	inPlace := runningJob("b", slotSource, domain.RestoreModeInPlace, "time", "T1")
	inPlace.NewProjectID = slotSource
	if err := jobs.StartRestoreJob(ctx, inPlace, storage.ProjectSlot{}); err != nil {
		t.Fatalf("an in-place restore needs no slot: %v", err)
	}
	if held, _ := store.CountOrgProjects(slotOrg); held != 2 {
		t.Fatalf("held slots = %d, want 2: an in-place restore holds none", held)
	}
}

// The hold belongs to the organisation the restore was admitted in, whatever
// happens to the source while the copy is built.
func TestRestoreIntoNewProject_HoldsTheSlotAfterTheSourceIsGone(t *testing.T) {
	store, jobs := slotStore(t)
	ctx := context.Background()
	if err := jobs.StartRestoreJob(ctx, copyJob("a", "T1"), storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(slotSource); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-slot0one", slotOrg), 2); err != nil {
		t.Fatalf("the source's slot is free again: %v", err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-slot0two", slotOrg), 2); !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("the running copy still holds its slot, got %v", err)
	}
}

// A copy whose row is being torn down while its job still runs holds its slot
// through the job until the job ends.
func TestRestoreIntoNewProject_CopyBeingDeletedStillHoldsThroughItsJob(t *testing.T) {
	store, jobs := slotStore(t)
	job := copyJob("a", "T1")
	if err := jobs.StartRestoreJob(context.Background(), job, storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2}); err != nil {
		t.Fatal(err)
	}
	target := instanceRow(job.NewProjectID, slotOrg)
	target.Status = string(domain.StatusDeleting)
	if err := store.Create(target); err != nil {
		t.Fatal(err)
	}
	if held, _ := store.CountOrgProjects(slotOrg); held != 2 {
		t.Fatalf("held slots = %d, want the source and the running copy", held)
	}
}

// Bringing a deleted project back needs a free slot, and a running copy holds one.
func TestRestoreIntoNewProject_BlocksBringingBackADeletedProject(t *testing.T) {
	store, jobs := slotStore(t)
	deleted := instanceRow("proj-slot0del", slotOrg)
	deleted.Status = string(domain.StatusPendingDeletion)
	if err := store.Create(deleted); err != nil {
		t.Fatal(err)
	}
	if err := jobs.StartRestoreJob(context.Background(), copyJob("a", "T1"), storage.ProjectSlot{OrgID: slotOrg, MaxProjects: 2}); err != nil {
		t.Fatal(err)
	}
	back := instanceRow("proj-slot0del", slotOrg)
	err := store.UpdateIfStatusWithinOrgLimit(back, string(domain.StatusPendingDeletion), 2)
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("bringing a project back into the copy's slot = %v, want the limit", err)
	}
}

// A copy and a new project racing for the last slot: exactly one gets it.
func TestRestoreIntoNewProject_RacesACreateForTheLastSlot(t *testing.T) {
	store, jobs := slotStore(t)
	ctx := context.Background()
	for round := 0; round < 10; round++ {
		org := fmt.Sprintf("org-race%02d", round)
		source := fmt.Sprintf("proj-race%02ds", round)
		if err := store.Create(instanceRow(source, org)); err != nil {
			t.Fatal(err)
		}
		job := runningJob(fmt.Sprintf("race-%02d", round), source, domain.RestoreModeNewProject, "latest", "")
		var wg sync.WaitGroup
		var restoreErr, createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			restoreErr = jobs.StartRestoreJob(ctx, job, storage.ProjectSlot{OrgID: org, MaxProjects: 2})
		}()
		go func() {
			defer wg.Done()
			createErr = store.CreateWithinOrgLimit(instanceRow(fmt.Sprintf("proj-race%02dn", round), org), 2)
		}()
		wg.Wait()
		if (restoreErr == nil) == (createErr == nil) {
			t.Fatalf("round %d: restore %v, create %v; exactly one must get the last slot", round, restoreErr, createErr)
		}
	}
}
