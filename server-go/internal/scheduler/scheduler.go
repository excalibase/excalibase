// Package scheduler implements Phase 8 deferred-execution primitives for
// Excalibase Functions:
//
//   - a Postgres-backed task queue
//     (`excalibase.excalibase_scheduled_functions`) populated by the
//     runtime's `ctx.scheduler.runAfter/runAt/cancel` RPCs and drained by
//     `Worker` via FOR UPDATE SKIP LOCKED polling;
//
//   - a Postgres-backed cron registry (`excalibase.excalibase_cron_jobs`)
//     populated by the bundler at deploy time and walked by `CronRunner`
//     to enqueue the next due task row per job.
//
// Both tables live in the reserved `excalibase` schema, never in the
// tenant's `public` schema, which is user space exposed through the
// generated APIs and Studio.
//
// # The tenant owns these tables
//
// Both tables live in the tenant's own database, and the tenant holds that
// database's credentials. The platform creates the reserved `excalibase`
// schema there, but it cannot protect it: a project can insert, update and
// delete any row in it. So nothing read out of these tables is authority —
// it is input.
//
//   - the project a task runs as comes from the sweep (the project whose
//     pool produced the row), never from the row's own project_id;
//   - a module name may only name a function the platform itself deployed
//     for that project, and must be a plain identifier;
//   - args must be JSON within the platform's size limit;
//   - attempt counts cannot widen the platform's retry budget;
//   - cron cadences are clipped to the platform minimum and the number of
//     jobs read per tick is capped.
//
// A row that fails any of these is closed with fixed platform text and
// never dispatched. See row_guard.go, which is where tenant content stops
// being input.
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
	"errors"

	"github.com/excalibase/provisioning-poc/internal/platformdb"
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

// ErrNotServable is what an Invoker returns (wrapped) when the task's
// project must not be served — a teardown or an unconfirmed restore owns
// it. The worker closes such a task as skipped rather than retrying: the
// project is not coming back on this task's retry budget.
var ErrNotServable = errors.New("project is not servable")

// EnsureTables creates the scheduler bookkeeping tables in the reserved
// `excalibase` schema and moves any legacy copies out of `public`. Called
// once per project database the first time a deploy with scheduling
// capability lands; idempotent, so callers may invoke it on every deploy
// without harm.
//
// The DDL itself lives in internal/platformdb — one source of truth shared
// with the deploy-time migrator, which applies exactly the same schema.
func EnsureTables(ctx context.Context, db *sql.DB) error {
	return platformdb.EnsureSchedulerTables(ctx, db)
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
		`UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'cancelled'
		 WHERE id = $1 AND status = 'pending'`,
		id,
	)
	return err
}
