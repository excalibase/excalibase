//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/logdelete"
)

// EXC-678: a deleted project's log deletion is queued once, leased to one
// worker at a time, and its progress survives a restart.
func TestProjectLogDeletions_QueueClaimAndSave(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-72 * time.Hour)
	row := logdelete.Deletion{ProjectID: "proj-a", Selector: `{namespace="org1-proj-a"}`, Start: start, End: now.Add(5 * time.Minute), NextAttemptAt: now}
	if err := store.EnqueueProjectLogDeletion(ctx, row); err != nil {
		t.Fatal(err)
	}
	// A retried teardown queues the same project again: the first row stays.
	again := row
	again.Start = start.Add(time.Hour)
	if err := store.EnqueueProjectLogDeletion(ctx, again); err != nil {
		t.Fatal(err)
	}
	due, err := store.ClaimDueProjectLogDeletions(ctx, now, 5*time.Minute, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %+v %v", due, err)
	}
	got := due[0]
	if got.ProjectID != "proj-a" || got.Selector != row.Selector || !got.Start.Equal(start) || !got.End.Equal(row.End) || got.State != logdelete.StatePending {
		t.Fatalf("claimed = %+v", got)
	}
	// Leased: another replica claims nothing.
	if other, err := store.ClaimDueProjectLogDeletions(ctx, now, 5*time.Minute, 10); err != nil || len(other) != 0 {
		t.Fatalf("leased row claimed again: %+v %v", other, err)
	}
	// Saved progress ends the lease and sets when it is next due.
	got.State, got.RequestedAt, got.NextAttemptAt = logdelete.StateRequested, now, now.Add(10*time.Minute)
	if err := store.SaveProjectLogDeletion(ctx, got); err != nil {
		t.Fatal(err)
	}
	if early, _ := store.ClaimDueProjectLogDeletions(ctx, now.Add(time.Minute), 5*time.Minute, 10); len(early) != 0 {
		t.Fatalf("claimed before due: %+v", early)
	}
	due, err = store.ClaimDueProjectLogDeletions(ctx, now.Add(10*time.Minute), 5*time.Minute, 10)
	if err != nil || len(due) != 1 || due[0].State != logdelete.StateRequested || !due[0].RequestedAt.Equal(now) {
		t.Fatalf("due after save = %+v %v", due, err)
	}
	// A lease that runs out (a replica died mid-work) frees the row, and the
	// worker that held it can no longer save over the new claim.
	expired, _ := store.ClaimDueProjectLogDeletions(ctx, now.Add(16*time.Minute), 5*time.Minute, 10)
	if len(expired) != 1 {
		t.Fatalf("expired lease not reclaimed: %+v", expired)
	}
	stale := due[0]
	stale.State = logdelete.StatePending
	if err := store.SaveProjectLogDeletion(ctx, stale); !errors.Is(err, logdelete.ErrLeaseLost) {
		t.Fatalf("a save under a lost lease = %v", err)
	}
	done := expired[0]
	done.Resends = 2
	done.State, done.DoneAt, done.LastError = logdelete.StateDone, now.Add(20*time.Minute), ""
	if err := store.SaveProjectLogDeletion(ctx, done); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.ClaimDueProjectLogDeletions(ctx, now.Add(48*time.Hour), 5*time.Minute, 10); len(after) != 0 {
		t.Fatalf("a done deletion is due again: %+v", after)
	}
	// A project id used again after a finished deletion is queued afresh.
	if err := store.EnqueueProjectLogDeletion(ctx, again); err != nil {
		t.Fatal(err)
	}
	due, _ = store.ClaimDueProjectLogDeletions(ctx, now.Add(48*time.Hour), 5*time.Minute, 10)
	if len(due) != 1 || due[0].State != logdelete.StatePending || !due[0].Start.Equal(again.Start) || due[0].Attempts != 0 || due[0].Resends != 0 {
		t.Fatalf("requeued = %+v", due)
	}
}

func TestProjectLogDeletions_ClaimLimitAndOrder(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"proj-a", "proj-b", "proj-c"} {
		if err := store.EnqueueProjectLogDeletion(ctx, logdelete.Deletion{ProjectID: id, Selector: `{project_id="` + id + `"}`, Start: now, End: now, NextAttemptAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ClaimDueProjectLogDeletions(ctx, now, time.Minute, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first = %+v %v", first, err)
	}
	rest, err := store.ClaimDueProjectLogDeletions(ctx, now, time.Minute, 2)
	if err != nil || len(rest) != 1 || rest[0].ProjectID == first[0].ProjectID || rest[0].ProjectID == first[1].ProjectID {
		t.Fatalf("rest = %+v %v", rest, err)
	}
}
