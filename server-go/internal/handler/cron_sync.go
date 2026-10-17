// Phase 8.5 — helpers to thread the cron-job sync transaction across
// the function deploy. Pulled out of function.go to keep that file
// focused on the HTTP layer.
package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// beginCronSync opens a transaction on the project DB as the scheduler's
// login and synchronises the bundle's cron registry with the cron_jobs table
// inside that transaction. Registration created the table; this login could
// not create it (EXC-623). Returns the open
// transaction and a commit callback so the caller can commit only on
// successful deploy.
//
// When the handler has no scheduler resolver (h.schedulerDBFn == nil) or
// the bundle declared no crons, this returns (nil, no-op, nil) — the
// caller still drives the same flow but the rollback path is a no-op.
func (h *FunctionHandler) beginCronSync(
	ctx context.Context,
	projectID, functionID string,
	cronJobs json.RawMessage,
) (*sql.Tx, func() error, error) {
	noop := func() error { return nil }
	// No project DB wired (tests + EXCALIBASE_AUTO_MIGRATE=false) — skip
	// the sync but stay on the same code path so a redeploy that drops
	// crons doesn't accidentally leave a stale row table behind in
	// environments that do have a DB.
	if h.schedulerDBFn == nil {
		return nil, noop, nil
	}
	db, err := h.schedulerDBFn(ctx, projectID)
	if errors.Is(err, domain.ErrPostgresOnly) && !declaresCronJobs(cronJobs) {
		// A MySQL project has no cron tables, and this function adds none.
		return nil, noop, nil
	}
	if err != nil {
		return nil, noop, fmt.Errorf("open project db: %w", err)
	}
	// Empty registry path: still open a tx so the redeploy-without-crons
	// case can DELETE this function's rows atomically with the deploy.
	var rows []scheduler.CronJobRow
	if len(cronJobs) > 0 {
		if err := json.Unmarshal(cronJobs, &rows); err != nil {
			return nil, noop, fmt.Errorf("parse cron registry: %w", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, noop, fmt.Errorf("begin cron tx: %w", err)
	}
	if err := scheduler.SyncCronJobs(ctx, tx, projectID, functionID, rows); err != nil {
		_ = tx.Rollback()
		return nil, noop, fmt.Errorf("sync cron jobs: %w", err)
	}
	commit := func() error {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit cron tx: %w", err)
		}
		return nil
	}
	return tx, commit, nil
}

// rollbackCronSync is the cleanup path called when the runtime deploy
// fails. Safe to call with a nil tx (the no-DB path).
func rollbackCronSync(tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	if err := tx.Rollback(); err != nil {
		log.Printf("WARN: cron sync rollback: %v", err)
		return err
	}
	return nil
}

// declaresCronJobs reports whether a bundle's cron registry names any job.
func declaresCronJobs(cronJobs json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(cronJobs))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}
