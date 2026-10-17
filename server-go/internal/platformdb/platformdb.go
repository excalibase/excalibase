// Package platformdb owns the DDL for the platform's own bookkeeping
// objects inside a tenant database.
//
// A tenant's `public` schema is user space: the app role holds ALL on it
// and everything there is exposed through GraphQL, REST and Studio.
// Platform metadata therefore lives in the reserved `excalibase` schema,
// which the generated APIs do not expose.
//
// The package is deliberately dependency-free so both the deploy-time
// migrator (internal/edgefn) and the runtime worker (internal/scheduler)
// can share one source of truth for this DDL without importing each other.
package platformdb

import (
	"context"
	"database/sql"
)

// ReservedSchema is the Postgres schema that holds platform bookkeeping
// objects in every tenant database. Never `public`.
const ReservedSchema = "excalibase"

// Table names of the Phase 8 scheduler bookkeeping objects. They are kept
// here for tests and log messages; the SQL below spells them out as
// literals rather than composing statements from these constants, so no
// statement in this package is dynamically built.
const (
	scheduledFunctionsTable = "excalibase_scheduled_functions"
	cronJobsTable           = "excalibase_cron_jobs"
)

// SchedulerTablesSQL is the single source of truth for the scheduler/cron
// bookkeeping schema. It is run by the superuser when a project is
// registered, so the superuser owns both tables and the only writer granted
// on them is the excalibase_scheduler login (EXC-623). Every statement is
// idempotent:
//
//   - the tables and the due-task index are created if absent;
//   - `excalibase_cron_jobs.function_id` is added if absent, upgrading
//     databases created before deploy-scoped cron sync existed;
//   - `excalibase_scheduled_functions.claimed_at` is added if absent. A
//     database that has not had this applied makes every sweep statement
//     fail on the unknown column, which is deliberate: the sweep says so and
//     backs the project off rather than guessing a claim is fresh.
//
// Nothing is ever moved in from `public`: a table there belongs to the
// customer, and moving it would bring a customer-owned object, with whatever
// triggers it carries, into the platform's schema.
//
//	excalibase.excalibase_scheduled_functions
//	  id              text PRIMARY KEY
//	  project_id      text NOT NULL
//	  module_name     text NOT NULL
//	  export_name     text NOT NULL
//	  args            jsonb NOT NULL
//	  scheduled_for   timestamptz NOT NULL
//	  status          text NOT NULL DEFAULT 'pending'
//	  attempts        int NOT NULL DEFAULT 0
//	  last_error      text
//	  created_at      timestamptz NOT NULL DEFAULT now()
//	  claimed_at      timestamptz     -- when a worker took the row
//
//	excalibase.excalibase_cron_jobs
//	  name             text NOT NULL
//	  project_id       text NOT NULL
//	  function_id      text NOT NULL   -- which function owns the row
//	  module_name      text NOT NULL
//	  export_name      text NOT NULL
//	  args             jsonb NOT NULL
//	  schedule         jsonb NOT NULL
//	  last_enqueued_at timestamptz
//	  PRIMARY KEY (project_id, name)
//
// `function_id` is written by SyncCronJobs at deploy time so a redeploy can
// scope its DELETE/UPSERT to rows owned by the same function.
const SchedulerTablesSQL = `
	CREATE SCHEMA IF NOT EXISTS excalibase;
	CREATE TABLE IF NOT EXISTS excalibase.excalibase_scheduled_functions (
		id text PRIMARY KEY,
		project_id text NOT NULL,
		module_name text NOT NULL,
		export_name text NOT NULL,
		args jsonb NOT NULL,
		scheduled_for timestamptz NOT NULL,
		status text NOT NULL DEFAULT 'pending',
		attempts int NOT NULL DEFAULT 0,
		last_error text,
		created_at timestamptz NOT NULL DEFAULT now(),
		claimed_at timestamptz
	);
	ALTER TABLE excalibase.excalibase_scheduled_functions
		ADD COLUMN IF NOT EXISTS claimed_at timestamptz;
	CREATE INDEX IF NOT EXISTS excalibase_scheduled_functions_due_idx
		ON excalibase.excalibase_scheduled_functions (status, scheduled_for)
		WHERE status = 'pending';
	CREATE TABLE IF NOT EXISTS excalibase.excalibase_cron_jobs (
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
	ALTER TABLE excalibase.excalibase_cron_jobs
		ADD COLUMN IF NOT EXISTS function_id text NOT NULL DEFAULT '';
`

// EnsureSchedulerTables applies SchedulerTablesSQL over db. It needs a role
// that may create in the excalibase schema, which in a tenant database is the
// superuser alone; registration runs the same SQL there.
func EnsureSchedulerTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, SchedulerTablesSQL)
	return err
}
