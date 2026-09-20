//go:build integration

package scheduler

import (
	"context"
	"testing"
	"time"
)

// A replica that dies mid-dispatch leaves the row 'running'. Nothing else
// ever looked at those rows, so the task was simply lost.
func TestWorker_AbandonedClaimIsReclaimedAndRun(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status, attempts, claimed_at)
		VALUES
		  ('stuck', 'proj_a', 'jobs', 'send', '{}', now() - interval '1 hour', 'running', 0,
		   now() - interval '1 hour')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	inv := &stubInvoker{}
	w := NewWorker(WorkerConfig{DB: db, ProjectID: "proj_a", Functions: allModules{},
		Invoker: inv, ClaimLease: time.Minute})

	// The first tick reaps the row back to pending; the second runs it.
	for i := 0; i < 2; i++ {
		if err := w.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(inv.Calls()) != 1 {
		t.Fatalf("invocations: got %d, want 1", len(inv.Calls()))
	}
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM excalibase.excalibase_scheduled_functions WHERE id = 'stuck'`).
		Scan(&status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "completed" {
		t.Errorf("status: got %q, want completed", status)
	}
}

// A row that kills whatever picks it up must not be reclaimed forever: each
// reap spends an attempt and the budget ends it.
func TestWorker_PoisonRowEndsFailedRatherThanLooping(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status, attempts, claimed_at)
		VALUES
		  ('poison', 'proj_a', 'jobs', 'send', '{}', now() - interval '1 hour', 'running', 2,
		   now() - interval '1 hour')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := NewWorker(WorkerConfig{DB: db, ProjectID: "proj_a", Functions: allModules{},
		Invoker: &stubInvoker{}, MaxAttempts: 3, ClaimLease: time.Minute})
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var status string
	var attempts int
	if err := db.QueryRowContext(ctx,
		`SELECT status, attempts FROM excalibase.excalibase_scheduled_functions WHERE id = 'poison'`).
		Scan(&status, &attempts); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "failed" || attempts != 3 {
		t.Errorf("row: got status=%q attempts=%d, want failed/3", status, attempts)
	}
}

// A claim in flight is not abandoned: a fresh claim time keeps the row out
// of the reap.
func TestWorker_FreshClaimIsNotReclaimed(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status, attempts, claimed_at)
		VALUES
		  ('fresh', 'proj_a', 'jobs', 'send', '{}', now() - interval '1 hour', 'running', 0, now())
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	inv := &stubInvoker{}
	w := NewWorker(WorkerConfig{DB: db, ProjectID: "proj_a", Functions: allModules{},
		Invoker: inv, ClaimLease: time.Hour})
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(inv.Calls()) != 0 {
		t.Fatalf("a claim still inside its lease was taken over: %v", inv.Calls())
	}
}
