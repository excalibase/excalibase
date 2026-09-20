package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"
)

// WorkerConfig wires the worker's collaborators.
type WorkerConfig struct {
	DB *sql.DB
	// ProjectID is the project whose database this worker was handed by the
	// sweep. It is the ONLY source of project identity: the tenant owns the
	// rows, so a row's own project_id is data to be checked, never authority.
	ProjectID string
	Invoker   Invoker
	// Functions is the platform's registry of deployed functions. A row may
	// only run a module the platform itself deployed for this project.
	Functions    FunctionRegistry
	PollInterval time.Duration
	// Batch caps the number of rows pulled per tick. Default 32.
	Batch int
	// MaxAttempts caps the number of times the worker re-runs a failing
	// task before giving up and marking it `failed`. Default 5.
	MaxAttempts int
	// MaxArgsBytes caps the size of a row's args. Default matches the public
	// invoke body limit.
	MaxArgsBytes int
	// MaxConcurrent caps in-flight invocations for this project (default 4);
	// Global, when set, caps them platform-wide across every project.
	MaxConcurrent int
	Global        *Semaphore
	// Logger is optional; defaults to the std log package.
	Logger *log.Logger
}

// Worker drains pending entries from `excalibase.excalibase_scheduled_functions`. One
// worker per replica; FOR UPDATE SKIP LOCKED keeps concurrent workers
// from double-firing a row.
type Worker struct {
	db           *sql.DB
	projectID    string
	invoker      Invoker
	functions    FunctionRegistry
	poll         time.Duration
	batch        int
	maxAttempts  int
	maxArgsBytes int
	local        *Semaphore
	global       *Semaphore
	logger       *log.Logger
}

// NewWorker constructs a Worker. Sensible defaults apply if WorkerConfig
// leaves a field at its zero value.
func NewWorker(c WorkerConfig) *Worker {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	poll := c.PollInterval
	if poll <= 0 {
		poll = 5 * time.Second
	}
	batch := c.Batch
	if batch <= 0 {
		batch = 32
	}
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	maxArgs := c.MaxArgsBytes
	if maxArgs <= 0 {
		maxArgs = DefaultMaxArgsBytes
	}
	concurrent := c.MaxConcurrent
	if concurrent <= 0 {
		concurrent = DefaultProjectConcurrency
	}
	return &Worker{
		db:           c.DB,
		projectID:    c.ProjectID,
		invoker:      c.Invoker,
		functions:    c.Functions,
		poll:         poll,
		batch:        batch,
		maxAttempts:  maxAttempts,
		maxArgsBytes: maxArgs,
		local:        NewSemaphore(concurrent),
		global:       c.Global,
		logger:       logger,
	}
}

// Run polls until the context is cancelled. Returns nil on graceful
// shutdown; any error inside a tick is logged but does not stop Run —
// transient DB errors should not take the worker out.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := w.Tick(ctx); err != nil {
				w.logger.Printf("scheduler.Worker tick: %v", err)
			}
		}
	}
}

// Tick performs one polling cycle: pull up to `batch` due rows, dispatch
// each in its own goroutine, and wait for the batch to drain before
// returning. Exported so tests can drive a deterministic cycle.
func (w *Worker) Tick(ctx context.Context) error {
	rows, err := w.claimDue(ctx)
	if err != nil {
		return fmt.Errorf("claim due: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	for _, r := range rows {
		if reason := w.reject(r); reason != "" {
			w.markRefused(ctx, r, reason)
			continue
		}
		if err := w.acquire(ctx); err != nil {
			// Shutting down: leave the rest of the batch claimed; the
			// reaper-free design means they are picked up again after the
			// row's lock is released on restart.
			break
		}
		wg.Add(1)
		go func(t pendingRow) {
			defer wg.Done()
			defer w.release()
			w.dispatch(ctx, t)
		}(r)
	}
	wg.Wait()
	return nil
}

// acquire takes a slot in the per-project pool and, when one is wired, the
// platform-wide pool. Releasing happens in the reverse order.
func (w *Worker) acquire(ctx context.Context) error {
	if w.global != nil {
		if err := w.global.Acquire(ctx); err != nil {
			return err
		}
	}
	if err := w.local.Acquire(ctx); err != nil {
		if w.global != nil {
			w.global.Release()
		}
		return err
	}
	return nil
}

func (w *Worker) release() {
	w.local.Release()
	if w.global != nil {
		w.global.Release()
	}
}

// reject decides whether a claimed row may run at all. Everything it looks
// at was written by the tenant, so each check answers "is this row allowed
// to select a security context?" rather than "is this row well-formed":
//
//   - project_id must be the project the sweep opened this database for;
//   - module/export must be plain identifiers, and the module must name a
//     function the platform deployed for THIS project;
//   - args must be valid JSON within the platform's size limit.
//
// A non-empty return value is the platform's own reason text; the row is
// closed with it and never dispatched.
func (w *Worker) reject(r pendingRow) string {
	if r.Invalid {
		return rejectMalformedRow
	}
	if w.projectID == "" || r.ProjectID != w.projectID {
		return rejectForeignProject
	}
	if !validModuleName(r.ModuleName) || !validExportName(r.ExportName) {
		return rejectBadIdentifier
	}
	if len(r.Args) > w.maxArgsBytes || !json.Valid(r.Args) {
		return rejectBadArgs
	}
	if w.functions == nil {
		return rejectUnknownFunction
	}
	known, err := w.functions.HasFunction(w.projectID, r.ModuleName)
	if err != nil || !known {
		return rejectUnknownFunction
	}
	return ""
}

// markRefused closes a row the platform will not run. The recorded reason
// is fixed platform text — tenant content is never echoed back.
func (w *Worker) markRefused(ctx context.Context, r pendingRow, reason string) {
	if _, err := w.db.ExecContext(ctx, `
		UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'failed', last_error = $2
		 WHERE id = $1
	`, r.ID, reason); err != nil {
		w.logger.Printf("scheduler: refuse task in %s: %v", w.projectID, err)
	}
}

// pendingRow mirrors the columns claimDue selects out of
// `excalibase.excalibase_scheduled_functions`.
type pendingRow struct {
	ID         string
	ProjectID  string
	ModuleName string
	ExportName string
	Args       json.RawMessage
	Attempts   int
	// Invalid marks a row whose columns could not be read as the schema
	// promises — a NULL or wrongly-typed value the tenant put there. Such a
	// row is closed, and the sweep carries on with the rest of the batch.
	Invalid bool
}

// closeOversizedSQL fails the due rows that exceed the platform's bounds,
// matching them on the columns' lengths alone. It never selects args, so a
// tenant cannot make the sweep read a payload by writing an enormous one.
const closeOversizedSQL = `
		UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'failed', last_error = $6
		 WHERE ctid IN (
		       SELECT ctid
		         FROM excalibase.excalibase_scheduled_functions
		        WHERE status = 'pending' AND scheduled_for <= now()
		          AND NOT (` + sizeBoundsSQL + `)
		        ORDER BY scheduled_for
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED)
	`

// claimDueSQL selects the due rows that are within bounds. The size
// predicate is here rather than in Go so an oversized row is refused by
// Postgres before any of it crosses the wire.
const claimDueSQL = `
		SELECT id, project_id, module_name, export_name, args, attempts
		  FROM excalibase.excalibase_scheduled_functions
		 WHERE status = 'pending' AND scheduled_for <= now()
		   AND ` + sizeBoundsSQL + `
		 ORDER BY scheduled_for
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED
	`

// claimDue atomically transitions up to `batch` pending rows whose
// scheduled_for ≤ now() to status='running' and returns them. The
// transaction holds row locks via FOR UPDATE SKIP LOCKED so concurrent
// workers see disjoint batches.
func (w *Worker) claimDue(ctx context.Context) ([]pendingRow, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	closeArgs := append([]any{w.batch}, w.sizeBoundArgs()...)
	if _, err := tx.ExecContext(ctx, closeOversizedSQL, append(closeArgs, rejectOversizedRow)...); err != nil {
		return nil, err
	}

	rs, err := tx.QueryContext(ctx, claimDueSQL, closeArgs...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	var out []pendingRow
	for rs.Next() {
		r, err := scanPendingRow(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, tx.Commit()
	}
	ids := make([]string, 0, len(out))
	for _, r := range out {
		ids = append(ids, r.ID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'running'
		 WHERE id = ANY($1::text[])
	`, asTextArray(ids)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// dispatch invokes the user function and finalises the row based on the
// outcome. On success the row is marked `completed`; on failure the row
// either bumps `attempts` + reschedules with exponential backoff, or is
// marked `failed` when MaxAttempts is reached.
func (w *Worker) dispatch(ctx context.Context, r pendingRow) {
	// The project id comes from the sweep, never from the row.
	err := w.invoker.Invoke(ctx, w.projectID, r.ModuleName, r.ExportName, r.Args)
	if err == nil {
		if _, dbErr := w.db.ExecContext(ctx, `
			UPDATE excalibase.excalibase_scheduled_functions
			   SET status = 'completed', attempts = attempts + 1
			 WHERE id = $1
		`, r.ID); dbErr != nil {
			w.logger.Printf("scheduler: mark completed %s: %v", r.ID, dbErr)
		}
		return
	}
	if errors.Is(err, ErrNotServable) {
		w.markSkipped(ctx, r, err)
		return
	}
	nextAttempts := clampAttempts(r.Attempts, w.maxAttempts) + 1
	if nextAttempts >= w.maxAttempts {
		if _, dbErr := w.db.ExecContext(ctx, `
			UPDATE excalibase.excalibase_scheduled_functions
			   SET status = 'failed', attempts = $2, last_error = $3
			 WHERE id = $1
		`, r.ID, nextAttempts, err.Error()); dbErr != nil {
			w.logger.Printf("scheduler: mark failed %s: %v", r.ID, dbErr)
		}
		return
	}
	backoff := backoffDelay(nextAttempts)
	if _, dbErr := w.db.ExecContext(ctx, `
		UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'pending',
		       attempts = $2,
		       last_error = $3,
		       scheduled_for = now() + ($4::int * interval '1 second')
		 WHERE id = $1
	`, r.ID, nextAttempts, err.Error(), int(backoff.Seconds())); dbErr != nil {
		w.logger.Printf("scheduler: reschedule %s: %v", r.ID, dbErr)
	}
}

// markSkipped closes a task whose project may not be served. The attempt
// is not counted: nothing was dispatched, and the reason is recorded so an
// operator reading the row sees why it never ran.
func (w *Worker) markSkipped(ctx context.Context, r pendingRow, cause error) {
	if _, dbErr := w.db.ExecContext(ctx, `
		UPDATE excalibase.excalibase_scheduled_functions
		   SET status = 'skipped', last_error = $2
		 WHERE id = $1
	`, r.ID, cause.Error()); dbErr != nil {
		w.logger.Printf("scheduler: mark skipped %s: %v", r.ID, dbErr)
	}
}

// backoffDelay returns the time to add before the next attempt. Capped at
// 5 minutes so a permanently-broken task doesn't drift into the far
// future. Formula: 2^(attempt-1) seconds + jitter-free.
func backoffDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	seconds := math.Pow(2, float64(attempt-1))
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// asTextArray converts a Go string slice into the postgres text[] literal
// the driver expects when bound through a `text[]` parameter. lib/pq
// supports this directly; we wrap to keep the SQL string readable.
func asTextArray(ids []string) any {
	// lib/pq accepts a `pq.Array` helper, but to avoid adding an import
	// for this single call we hand-build the array literal. The string
	// contents (task ids) are base32 emitted by the runtime, so escaping
	// concerns are limited; we still quote them to be safe.
	buf := []byte{'{'}
	for i, s := range ids {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '"')
		for _, c := range []byte(s) {
			if c == '"' || c == '\\' {
				buf = append(buf, '\\')
			}
			buf = append(buf, c)
		}
		buf = append(buf, '"')
	}
	buf = append(buf, '}')
	return string(buf)
}
