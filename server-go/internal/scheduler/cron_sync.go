// Phase 8.5 — deploy-time cron sync.
//
// Function bundles declare their cron registry via `cronJobs()` from
// @excalibase/server. The bundler captures the registry as a JSON array
// of `{name, schedule, fnRef, args}` rows on Function.CronJobs. At deploy
// time the platform syncs that array to the `excalibase.excalibase_cron_jobs` table
// so the CronRunner can find each job at its next due time.
//
// The sync runs inside the caller-supplied transaction so it commits or
// rolls back together with the rest of the deploy. Idempotent: deploying
// the same bundle twice is a no-op net change.

package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// CronJobRow models one entry in Function.CronJobs.
type CronJobRow struct {
	Name     string          `json:"name"`
	Schedule json.RawMessage `json:"schedule"`
	FnRef    struct {
		ModuleName string `json:"moduleName"`
		ExportName string `json:"exportName"`
	} `json:"fnRef"`
	Args json.RawMessage `json:"args"`
}

// SyncCronJobs replaces every row owned by (projectID, functionID) with
// the supplied list, atomically inside `tx`. Behaviour:
//
//   - rows whose name is no longer in `jobs` are DELETEd
//   - rows whose name is present are UPSERTed (schedule, args, fnRef may
//     have changed)
//   - rows owned by a different function in the same project are left
//     alone — cron names are globally unique within a project (the
//     primary key enforces it) but cleanup is per-function so a deploy
//     of fn-A doesn't disturb crons owned by fn-B
//
// `jobs` may be nil/empty — in that case all rows owned by this function
// are deleted (the "redeploy without crons.ts" case).
//
// The function ID is required so deletions stay scoped; cron names are
// not globally unique across functions in the original Phase 8 table.
// The function_id column is added by EnsureTables.
func SyncCronJobs(
	ctx context.Context,
	tx *sql.Tx,
	projectID, functionID string,
	jobs []CronJobRow,
) error {
	if projectID == "" {
		return fmt.Errorf("sync cron: project id is required")
	}
	if functionID == "" {
		return fmt.Errorf("sync cron: function id is required")
	}

	// Build the set of names that should survive after this sync.
	wantedNames := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		if j.Name == "" {
			return fmt.Errorf("sync cron: row missing name")
		}
		wantedNames[j.Name] = struct{}{}
	}

	// 1. DELETE rows owned by this function whose name is NOT in `jobs`.
	//    Done before the UPSERT loop so a renamed cron (old name → new
	//    name) doesn't accidentally leave both rows live.
	if len(wantedNames) == 0 {
		return deleteAllCronRows(ctx, tx, projectID, functionID)
	}
	if err := deleteStaleCronRows(ctx, tx, projectID, functionID, wantedNames); err != nil {
		return err
	}

	// 2. UPSERT every row in the bundle.
	return upsertCronRows(ctx, tx, projectID, functionID, jobs)
}

// deleteAllCronRows clears every cron row owned by this function — the
// "redeploy without crons.ts" case where no schedules should survive.
func deleteAllCronRows(ctx context.Context, tx *sql.Tx, projectID, functionID string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM excalibase.excalibase_cron_jobs
		 WHERE project_id = $1 AND function_id = $2
	`, projectID, functionID); err != nil {
		return fmt.Errorf("sync cron: clear function rows: %w", err)
	}
	return nil
}

// deleteStaleCronRows removes rows owned by this function whose name is not in
// wantedNames, so a renamed cron doesn't leave its old row live.
func deleteStaleCronRows(ctx context.Context, tx *sql.Tx, projectID, functionID string, wantedNames map[string]struct{}) error {
	names := make([]string, 0, len(wantedNames))
	for n := range wantedNames {
		names = append(names, n)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM excalibase.excalibase_cron_jobs
		 WHERE project_id = $1
		   AND function_id = $2
		   AND name <> ALL($3::text[])
	`, projectID, functionID, asTextArray(names)); err != nil {
		return fmt.Errorf("sync cron: delete stale rows: %w", err)
	}
	return nil
}

// upsertCronRows inserts or updates every row in the bundle. The PRIMARY KEY
// (project_id, name) drives the ON CONFLICT clause; function_id is set on
// insert so the next sync can scope its delete back to us.
func upsertCronRows(ctx context.Context, tx *sql.Tx, projectID, functionID string, jobs []CronJobRow) error {
	for _, j := range jobs {
		args := j.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if len(j.Schedule) == 0 {
			return fmt.Errorf("sync cron: row %q missing schedule", j.Name)
		}
		if j.FnRef.ModuleName == "" || j.FnRef.ExportName == "" {
			return fmt.Errorf("sync cron: row %q missing fnRef", j.Name)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO excalibase.excalibase_cron_jobs
			  (name, project_id, function_id, module_name, export_name, args, schedule)
			VALUES
			  ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (project_id, name) DO UPDATE
			  SET function_id  = EXCLUDED.function_id,
			      module_name  = EXCLUDED.module_name,
			      export_name  = EXCLUDED.export_name,
			      args         = EXCLUDED.args,
			      schedule     = EXCLUDED.schedule
		`, j.Name, projectID, functionID,
			j.FnRef.ModuleName, j.FnRef.ExportName,
			[]byte(args), []byte(j.Schedule)); err != nil {
			return fmt.Errorf("sync cron: upsert row %q: %w", j.Name, err)
		}
	}
	return nil
}
