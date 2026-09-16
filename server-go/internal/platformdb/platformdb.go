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

// schedulerDDL is the single source of truth for the scheduler/cron
// bookkeeping schema. Every statement is idempotent, so callers may run it
// on every deploy:
//
//   - the reserved schema is created if absent;
//   - tables left in `public` by pre-reserved-schema releases are moved
//     into it, guarded so the move is a no-op on a fresh tenant (nothing
//     to move) and on an already-migrated one (destination occupied);
//   - the tables and the due-task index are created if absent;
//   - `excalibase_cron_jobs.function_id` is added if absent, upgrading
//     databases created before deploy-scoped cron sync existed.
//
// Ordering matters: the move runs before the CREATE TABLE statements,
// otherwise a fresh empty table would occupy the destination and strand
// the legacy rows in `public`.
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
const schedulerDDL = `
	-- The create stays behind a catalog guard because the IF NOT EXISTS form
	-- still checks CREATE on the database even when the schema is already
	-- there, and the tenant app role deliberately lacks that. Provisioning
	-- pre-creates the schema, so this only fires on a database provisioned
	-- before it did.
	DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'excalibase') THEN
			EXECUTE 'CREATE SCHEMA excalibase';
		END IF;
	END $$;
	DO $$
	BEGIN
		IF to_regclass('public.excalibase_scheduled_functions') IS NOT NULL
		   AND to_regclass('excalibase.excalibase_scheduled_functions') IS NULL THEN
			ALTER TABLE public.excalibase_scheduled_functions SET SCHEMA excalibase;
		END IF;
		IF to_regclass('public.excalibase_cron_jobs') IS NOT NULL
		   AND to_regclass('excalibase.excalibase_cron_jobs') IS NULL THEN
			ALTER TABLE public.excalibase_cron_jobs SET SCHEMA excalibase;
		END IF;
	END $$;
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
		created_at timestamptz NOT NULL DEFAULT now()
	);
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

// EnsureSchedulerTables applies schedulerDDL to the given tenant database.
// Idempotent and safe to call on every deploy and on every worker boot.
func EnsureSchedulerTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, schedulerDDL)
	return err
}
