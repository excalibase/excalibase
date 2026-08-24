// Package scheduler implements Phase 8 deferred-execution primitives for
// Excalibase Functions:
//
//   * a Postgres-backed task queue (`excalibase_scheduled_functions`)
//     populated by the runtime's `ctx.scheduler.runAfter/runAt/cancel`
//     RPCs and drained by `Worker` via FOR UPDATE SKIP LOCKED polling;
//
//   * a Postgres-backed cron registry (`excalibase_cron_jobs`) populated
//     by the bundler at deploy time and walked by `CronRunner` to enqueue
//     the next due `excalibase_scheduled_functions` row per job.
//
// The package is decoupled from any specific invoker shape — the `Invoker`
// interface is the only seam, satisfied at production-time by the
// runtime's internal-invoke HTTP route and at test-time by an in-memory
// stub.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Invoker is the seam between the scheduler worker and the runtime that
// runs user functions. The worker calls Invoke for each due task; the
// production implementation dispatches via the gateway's internal-invoke
// route, and tests substitute a recording stub.
//
// Implementations must be safe for concurrent use — the worker calls
// Invoke from goroutines spawned per pending row.
type Invoker interface {
	Invoke(ctx context.Context, projectID, moduleName, exportName string, args json.RawMessage) error
}

// EnsureTables creates the `excalibase_scheduled_functions` and
// `excalibase_cron_jobs` tables (idempotent, IF NOT EXISTS). Called once
// per project database the first time a deploy with scheduling capability
// lands. Callers may invoke it on every deploy without harm.
//
// The schema matches the Phase 8 spec:
//
//   excalibase_scheduled_functions
//     id              text PRIMARY KEY
//     project_id      text NOT NULL
//     module_name     text NOT NULL
//     export_name     text NOT NULL
//     args            jsonb NOT NULL
//     scheduled_for   timestamptz NOT NULL
//     status          text NOT NULL DEFAULT 'pending'
//     attempts        int NOT NULL DEFAULT 0
//     last_error      text
//     created_at      timestamptz NOT NULL DEFAULT now()
//
//   excalibase_cron_jobs
//     name             text NOT NULL
//     project_id       text NOT NULL
//     function_id      text NOT NULL   -- Phase 8.5: which function owns the row
//     module_name      text NOT NULL
//     export_name      text NOT NULL
//     args             jsonb NOT NULL
//     schedule         jsonb NOT NULL
//     last_enqueued_at timestamptz
//     PRIMARY KEY (project_id, name)
//
// `function_id` is added by SyncCronJobs at deploy time so a redeploy
// can scope its DELETE/UPSERT to rows owned by the same function.
func EnsureTables(ctx context.Context, db *sql.DB) error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS excalibase_scheduled_functions (
			id text PRIMARY KEY,
			project_id text NOT NULL,
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			scheduled_for timestamptz NOT NULL,
			status text NOT NULL DEFAULT 'pending',
			attempts int NOT NULL DEFAULT 0,
			last_error text,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS excalibase_scheduled_functions_due_idx
			ON excalibase_scheduled_functions (status, scheduled_for)
			WHERE status = 'pending';
		CREATE TABLE IF NOT EXISTS excalibase_cron_jobs (
			name text NOT NULL,
			project_id text NOT NULL,
			function_id text NOT NULL DEFAULT '',
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			schedule jsonb NOT NULL,
			last_enqueued_at timestamptz,
			PRIMARY KEY (project_id, name)
		);
		-- Idempotent ALTER for pre-Phase-8.5 databases (DEFAULT '' so
		-- existing rows scoot through the NOT NULL constraint).
		ALTER TABLE excalibase_cron_jobs
			ADD COLUMN IF NOT EXISTS function_id text NOT NULL DEFAULT '';
	`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return err
	}
	return nil
}

// Cancel marks a pending scheduled task as cancelled. Idempotent: if the
// row is missing, already running, completed, failed, or cancelled, the
// statement is a no-op.
//
// Convex parity: cancelling a row whose task already started lets the
// task continue, but any tasks IT schedules must be blocked. That second
// rule lives on the runtime side (the running task's scheduler RPC
// checks the parent row's status before inserting a child) — Cancel is
// strictly the "block the dispatch" half of the contract.
func Cancel(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE excalibase_scheduled_functions
		   SET status = 'cancelled'
		 WHERE id = $1 AND status = 'pending'`,
		id,
	)
	return err
}
