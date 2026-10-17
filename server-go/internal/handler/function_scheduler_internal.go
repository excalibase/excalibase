package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/go-chi/chi/v5"
)

// A project's ctx.scheduler calls reach its scheduler tables through this
// route. The tables sit in the platform's excalibase schema, which the
// project's own logins cannot write (EXC-623), so the runtime sends the calls
// here and provisioning writes them as the scheduler's login.

const (
	// maxScheduleOps caps the calls one mutation or action records at once.
	maxScheduleOps = 1000
	// maxScheduleBody bounds the request before any op is read.
	maxScheduleBody = 8 << 20
)

type scheduleOp struct {
	Op             string          `json:"op"`
	ID             string          `json:"id"`
	ModuleName     string          `json:"moduleName"`
	ExportName     string          `json:"exportName"`
	Args           json.RawMessage `json:"args"`
	ScheduledForMs *int64          `json:"scheduledForMs"`
	// compactArgs is Args with insignificant whitespace removed, so the size
	// limit is measured against what jsonb stores rather than the wire bytes.
	compactArgs []byte
}

var errBadScheduleOp = errors.New("invalid scheduler call")

// InternalSchedule serves POST /internal/runtime/projects/{projectId}/scheduler
// for the project's runtime, authenticated by that project's runtime token.
// The ops are applied in order in one transaction.
func (h *FunctionHandler) InternalSchedule(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if !h.authorizeRuntimeToken(w, r, projectID) {
		return
	}
	ops, err := decodeScheduleOps(w, r)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if h.schedulerDBFn == nil {
		httpError(w, "scheduler not configured", http.StatusServiceUnavailable)
		return
	}
	db, err := h.schedulerDBFn(r.Context(), projectID)
	if err != nil {
		log.Printf("scheduler: open %s as the scheduler login: %v", projectID, err)
		httpError(w, "scheduler unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := applyScheduleOps(r.Context(), db, projectID, ops); err != nil {
		log.Printf("scheduler: record calls for %s: %v", projectID, err)
		httpError(w, "could not record the scheduler calls", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeScheduleOps(w http.ResponseWriter, r *http.Request) ([]scheduleOp, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxScheduleBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		Ops []scheduleOp `json:"ops"`
	}
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.New(errInvalidRequestBody)
	}
	if len(body.Ops) == 0 || len(body.Ops) > maxScheduleOps {
		return nil, fmt.Errorf("%w: between 1 and %d calls per request", errBadScheduleOp, maxScheduleOps)
	}
	for i := range body.Ops {
		if err := validScheduleOp(body.Ops[i]); err != nil {
			return nil, fmt.Errorf("call %d: %w", i, err)
		}
		if body.Ops[i].Op != "schedule" {
			continue
		}
		// Measure the limit against the normalised form, so a task the worker
		// would accept is not refused here for its wire whitespace, nor one it
		// would silently close accepted here (the minor finding).
		var compact bytes.Buffer
		if err := json.Compact(&compact, body.Ops[i].Args); err != nil {
			return nil, fmt.Errorf("call %d: %w: args", i, errBadScheduleOp)
		}
		if compact.Len() > scheduler.DefaultMaxArgsBytes {
			return nil, fmt.Errorf("call %d: %w: args", i, errBadScheduleOp)
		}
		body.Ops[i].compactArgs = append([]byte(nil), compact.Bytes()...)
	}
	return body.Ops, nil
}

func validScheduleOp(op scheduleOp) error {
	if !scheduler.ValidTaskID(op.ID) {
		return fmt.Errorf("%w: id", errBadScheduleOp)
	}
	switch op.Op {
	case "cancel":
		return nil
	case "schedule":
	default:
		return fmt.Errorf("%w: op", errBadScheduleOp)
	}
	if !scheduler.ValidTarget(op.ModuleName, op.ExportName) {
		return fmt.Errorf("%w: module or export name", errBadScheduleOp)
	}
	if op.ScheduledForMs == nil {
		return fmt.Errorf("%w: scheduledForMs", errBadScheduleOp)
	}
	if len(bytes.TrimSpace(op.Args)) == 0 {
		return fmt.Errorf("%w: args", errBadScheduleOp)
	}
	return nil
}

func applyScheduleOps(ctx context.Context, db *sql.DB, projectID string, ops []scheduleOp) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := applyScheduleOp(ctx, tx, projectID, op); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// A retried call carries the same id, so a schedule already recorded is left
// as it is; a cancel reaches only this project's pending rows.
func applyScheduleOp(ctx context.Context, tx *sql.Tx, projectID string, op scheduleOp) error {
	if op.Op == "cancel" {
		_, err := tx.ExecContext(ctx, `UPDATE excalibase.excalibase_scheduled_functions
			SET status = 'cancelled'
			WHERE id = $1 AND project_id = $2 AND status = 'pending'`, op.ID, projectID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO excalibase.excalibase_scheduled_functions
			(id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, 'pending')
		ON CONFLICT (id) DO NOTHING`,
		op.ID, projectID, op.ModuleName, op.ExportName, string(op.compactArgs),
		time.UnixMilli(*op.ScheduledForMs).UTC())
	return err
}
