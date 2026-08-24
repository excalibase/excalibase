package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sync"
	"time"
)

// WorkerConfig wires the worker's collaborators.
type WorkerConfig struct {
	DB           *sql.DB
	Invoker      Invoker
	PollInterval time.Duration
	// Batch caps the number of rows pulled per tick. Default 32.
	Batch int
	// MaxAttempts caps the number of times the worker re-runs a failing
	// task before giving up and marking it `failed`. Default 5.
	MaxAttempts int
	// Logger is optional; defaults to the std log package.
	Logger *log.Logger
}

// Worker drains pending entries from `excalibase_scheduled_functions`. One
// worker per replica; FOR UPDATE SKIP LOCKED keeps concurrent workers
// from double-firing a row.
type Worker struct {
	db          *sql.DB
	invoker     Invoker
	poll        time.Duration
	batch       int
	maxAttempts int
	logger      *log.Logger
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
	return &Worker{
		db:          c.DB,
		invoker:     c.Invoker,
		poll:        poll,
		batch:       batch,
		maxAttempts: maxAttempts,
		logger:      logger,
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
		wg.Add(1)
		go func(t pendingRow) {
			defer wg.Done()
			w.dispatch(ctx, t)
		}(r)
	}
	wg.Wait()
	return nil
}

// pendingRow mirrors the columns claimDue selects out of
// `excalibase_scheduled_functions`.
type pendingRow struct {
	ID         string
	ProjectID  string
	ModuleName string
	ExportName string
	Args       json.RawMessage
	Attempts   int
}

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

	rs, err := tx.QueryContext(ctx, `
		SELECT id, project_id, module_name, export_name, args, attempts
		  FROM excalibase_scheduled_functions
		 WHERE status = 'pending' AND scheduled_for <= now()
		 ORDER BY scheduled_for
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED
	`, w.batch)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	var out []pendingRow
	for rs.Next() {
		var r pendingRow
		if err := rs.Scan(&r.ID, &r.ProjectID, &r.ModuleName, &r.ExportName, &r.Args, &r.Attempts); err != nil {
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
		UPDATE excalibase_scheduled_functions
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
	err := w.invoker.Invoke(ctx, r.ProjectID, r.ModuleName, r.ExportName, r.Args)
	if err == nil {
		if _, dbErr := w.db.ExecContext(ctx, `
			UPDATE excalibase_scheduled_functions
			   SET status = 'completed', attempts = attempts + 1
			 WHERE id = $1
		`, r.ID); dbErr != nil {
			w.logger.Printf("scheduler: mark completed %s: %v", r.ID, dbErr)
		}
		return
	}
	nextAttempts := r.Attempts + 1
	if nextAttempts >= w.maxAttempts {
		if _, dbErr := w.db.ExecContext(ctx, `
			UPDATE excalibase_scheduled_functions
			   SET status = 'failed', attempts = $2, last_error = $3
			 WHERE id = $1
		`, r.ID, nextAttempts, err.Error()); dbErr != nil {
			w.logger.Printf("scheduler: mark failed %s: %v", r.ID, dbErr)
		}
		return
	}
	backoff := backoffDelay(nextAttempts)
	if _, dbErr := w.db.ExecContext(ctx, `
		UPDATE excalibase_scheduled_functions
		   SET status = 'pending',
		       attempts = $2,
		       last_error = $3,
		       scheduled_for = now() + ($4::int * interval '1 second')
		 WHERE id = $1
	`, r.ID, nextAttempts, err.Error(), int(backoff.Seconds())); dbErr != nil {
		w.logger.Printf("scheduler: reschedule %s: %v", r.ID, dbErr)
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
