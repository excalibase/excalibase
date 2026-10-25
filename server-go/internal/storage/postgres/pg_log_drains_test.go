//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

var drainT0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func newTestDrain(id, projectID string) *domain.LogDrain {
	return &domain.LogDrain{ID: id, ProjectID: projectID, Name: "n-" + id, Kind: "datadog", DatadogSite: "us1",
		Enabled: true, Status: domain.LogDrainActive, CursorNS: drainT0.UnixNano(), NextAttemptAt: drainT0,
		CreatedBy: "u", CreatedAt: drainT0, UpdatedAt: drainT0}
}

func TestLogDrains_CRUDScopedToProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateLogDrain(ctx, newTestDrain("ld_a1", "pa"), 3); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.CreateLogDrain(ctx, newTestDrain("ld_b1", "pb"), 3); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetLogDrain(ctx, "pa", "ld_a1")
	if err != nil || got.Name != "n-ld_a1" || got.CursorNS != drainT0.UnixNano() || !got.Enabled {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := store.GetLogDrain(ctx, "pb", "ld_a1"); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("cross-project get: %v", err)
	}
	listed, err := store.ListLogDrains(ctx, "pa")
	if err != nil || len(listed) != 1 || listed[0].ID != "ld_a1" {
		t.Errorf("list = %+v, %v", listed, err)
	}

	other := *got
	other.ProjectID = "pb"
	other.Name = "hijack"
	if err := store.UpdateLogDrain(ctx, &other); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("cross-project update: %v", err)
	}
	// A settings change never touches delivery state.
	got.Name, got.Enabled, got.CursorNS = "renamed", false, 1
	if err := store.UpdateLogDrain(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if again, _ := store.FindLogDrain(ctx, "ld_a1"); again.Name != "renamed" || !again.Enabled || again.CursorNS != drainT0.UnixNano() {
		t.Errorf("after update %+v", again)
	}
	if err := store.SetLogDrainEnabled(ctx, "pa", "ld_a1", false, 0, drainT0); err != nil {
		t.Fatal(err)
	}
	if off, _ := store.FindLogDrain(ctx, "ld_a1"); off.Enabled || off.Status != domain.LogDrainDisabled || off.CursorNS != drainT0.UnixNano() {
		t.Errorf("after disable %+v", off)
	}
	later := drainT0.Add(time.Hour)
	if err := store.SetLogDrainEnabled(ctx, "pa", "ld_a1", true, later.UnixNano(), later); err != nil {
		t.Fatal(err)
	}
	if on, _ := store.FindLogDrain(ctx, "ld_a1"); !on.Enabled || on.Status != domain.LogDrainActive || on.CursorNS != later.UnixNano() {
		t.Errorf("after enable %+v", on)
	}
	if err := store.SetLogDrainEnabled(ctx, "pb", "ld_a1", true, 0, later); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("cross-project enable: %v", err)
	}
	if err := store.DeleteLogDrain(ctx, "pb", "ld_a1"); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("cross-project delete: %v", err)
	}
	if err := store.DeleteLogDrain(ctx, "pa", "ld_a1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.FindLogDrain(ctx, "ld_a1"); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

// The per-project limit holds under concurrent creates.
func TestLogDrains_LimitHoldsConcurrently(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- store.CreateLogDrain(ctx, newTestDrain("ld_c"+string(rune('a'+i)), "pc"), 3)
		}(i)
	}
	wg.Wait()
	close(results)
	created, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, domain.ErrLogDrainLimit):
			limited++
		default:
			t.Errorf("create: %v", err)
		}
	}
	if created != 3 || limited != 5 {
		t.Errorf("created %d, limited %d", created, limited)
	}
}

func TestLogDrains_DueAdvanceAndFail(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	due := newTestDrain("ld_due", "pa")
	later := newTestDrain("ld_later", "pa")
	later.NextAttemptAt = drainT0.Add(time.Hour)
	off := newTestDrain("ld_off", "pb")
	off.Enabled = false
	for _, drain := range []*domain.LogDrain{due, later, off} {
		if err := store.CreateLogDrain(ctx, drain, 3); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.DueLogDrains(ctx, drainT0.Add(time.Second), 10)
	if err != nil || len(got) != 1 || got[0].ID != "ld_due" {
		t.Fatalf("due = %+v, %v", got, err)
	}

	from := due.CursorNS
	fail := domain.LogDrainFailure{ID: "ld_due", From: from, Error: "the destination answered 500", Next: drainT0.Add(time.Minute)}
	if ok, err := store.FailLogDrain(ctx, fail); err != nil || !ok {
		t.Fatalf("fail = %v, %v", ok, err)
	}
	failed, _ := store.FindLogDrain(ctx, "ld_due")
	if failed.ConsecutiveFailures != 1 || failed.Status != domain.LogDrainFailing || failed.LastError == "" || !failed.Enabled {
		t.Errorf("after fail %+v", failed)
	}

	if ok, _ := store.AdvanceLogDrain(ctx, domain.LogDrainAdvance{ID: "ld_due", From: from + 1, To: from + 10}); ok {
		t.Error("an advance from a stale cursor applied")
	}
	unleased := domain.LogDrainAdvance{ID: "ld_due", From: from, To: from + 10, Sent: 1, Delivered: true, Next: drainT0}
	if ok, _ := store.AdvanceLogDrain(ctx, unleased); ok {
		t.Error("a delivery nobody leased applied")
	}
	lease := domain.LogDrainLease{ID: "ld_due", From: from, To: from + 10, Lines: 7, Until: drainT0.Add(time.Hour), Now: drainT0.Add(2 * time.Minute)}
	if ok, err := store.LeaseLogDrain(ctx, lease); err != nil || !ok {
		t.Fatalf("lease = %v, %v", ok, err)
	}
	if ok, _ := store.LeaseLogDrain(ctx, lease); ok {
		t.Error("a leased drain was handed out twice")
	}
	adv := domain.LogDrainAdvance{ID: "ld_due", From: from, To: from + 10, Sent: 7, Dropped: 2, Delivered: true, Next: drainT0.Add(2 * time.Minute)}
	if ok, err := store.AdvanceLogDrain(ctx, adv); err != nil || !ok {
		t.Fatalf("advance = %v, %v", ok, err)
	}
	moved, _ := store.FindLogDrain(ctx, "ld_due")
	if moved.CursorNS != from+10 || moved.SentLines != 7 || moved.DroppedLines != 2 || moved.ConsecutiveFailures != 0 ||
		moved.Status != domain.LogDrainActive || moved.LastError != "" || moved.LastSuccessAt == nil {
		t.Errorf("after advance %+v", moved)
	}
	// An undelivered advance (a skip) keeps the failure state.
	if _, err := store.FailLogDrain(ctx, domain.LogDrainFailure{ID: "ld_due", From: from + 10, Error: "e", Next: drainT0}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.AdvanceLogDrain(ctx, domain.LogDrainAdvance{ID: "ld_due", From: from + 10, To: from + 20, Dropped: 5, Next: drainT0}); !ok {
		t.Fatal("skip did not apply")
	}
	skipped, _ := store.FindLogDrain(ctx, "ld_due")
	if skipped.ConsecutiveFailures != 1 || skipped.DroppedLines != 7 || skipped.Status != domain.LogDrainFailing {
		t.Errorf("after skip %+v", skipped)
	}

	if ok, _ := store.FailLogDrain(ctx, domain.LogDrainFailure{ID: "ld_due", From: from + 20, LeaseTo: from + 99, Error: "x", Next: drainT0}); ok {
		t.Error("a failure for a lease the drain does not hold applied")
	}
	disable := domain.LogDrainFailure{ID: "ld_due", From: from + 20, Error: "gave up", Next: drainT0, Disable: true}
	if ok, _ := store.FailLogDrain(ctx, disable); !ok {
		t.Fatal("disable did not apply")
	}
	disabled, _ := store.FindLogDrain(ctx, "ld_due")
	if disabled.Enabled || disabled.Status != domain.LogDrainDisabled || disabled.LastError != "gave up" {
		t.Errorf("after disable %+v", disabled)
	}
	if ok, _ := store.FailLogDrain(ctx, disable); ok {
		t.Error("a disabled drain took another failure")
	}
	if err := store.DeferLogDrain(ctx, "ld_later", drainT0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if deferred, _ := store.FindLogDrain(ctx, "ld_later"); !deferred.NextAttemptAt.Equal(drainT0.Add(3 * time.Hour)) {
		t.Errorf("defer: %v", deferred.NextAttemptAt)
	}
}
