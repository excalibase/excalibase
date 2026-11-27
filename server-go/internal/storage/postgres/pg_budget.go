package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// SetPlanBudget wires the gate every step that grows an org's services is
// admitted through (EXC-901). Without one, nothing is checked.
func (s *Store) SetPlanBudget(gate *planbudget.Gate) { s.budget = gate }

// reservedRow is one project's database as the budget sees it.
type reservedRow struct {
	orgID, tier, status, storageSize string
	perCopy                          planbudget.Size
	copies                           int64
}

// held is what the row reserves now.
func (r reservedRow) held() planbudget.Size {
	perCopy := r.perCopy
	if disk, err := planbudget.ParseBytes(r.storageSize); err == nil && r.storageSize != "" {
		perCopy.DiskBytes = disk
	}
	return planbudget.ReservedDatabase(planbudget.Database{Status: r.status, PerCopy: perCopy, Copies: r.copies})
}

// readReservedRow locks and reads the project's row; the caller holds the org lock.
func readReservedRow(ctx context.Context, tx *sql.Tx, projectID string) (reservedRow, error) {
	var row reservedRow
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(org_id, ''), tier, status, COALESCE(storage_size, ''), reserved_cpu_milli, reserved_memory_bytes, reserved_disk_bytes, reserved_copies
		FROM database_instances WHERE project_id = $1 FOR UPDATE`, projectID).
		Scan(&row.orgID, &row.tier, &row.status, &row.storageSize, &row.perCopy.CPUMilli, &row.perCopy.MemoryBytes, &row.perCopy.DiskBytes, &row.copies)
	if errors.Is(err, sql.ErrNoRows) {
		return row, storage.ErrProjectNotFound
	}
	if err != nil {
		return row, fmt.Errorf("read project reservation: %w", err)
	}
	return row, nil
}

// tierReservation is the size one copy of a database on the tier reserves, and
// how many copies the tier runs. The tier's stored row decides, the built-in
// tier when none is stored.
func tierReservation(ctx context.Context, tx execQueryer, tier domain.TierType, storageSize string) (planbudget.Size, int64, error) {
	plan, err := tierPlan(ctx, tx, tier)
	if err != nil {
		return planbudget.Size{}, 0, err
	}
	cpu, err := planbudget.ParseCPUMilli(plan.CPU)
	if err != nil {
		return planbudget.Size{}, 0, fmt.Errorf("plan %s cpu: %w", tier, err)
	}
	memory, err := planbudget.ParseBytes(plan.Memory)
	if err != nil {
		return planbudget.Size{}, 0, fmt.Errorf("plan %s memory: %w", tier, err)
	}
	if storageSize == "" {
		storageSize = plan.StorageSize
	}
	disk, err := planbudget.ParseBytes(storageSize)
	if err != nil {
		return planbudget.Size{}, 0, fmt.Errorf("plan %s disk: %w", tier, err)
	}
	copies := int64(plan.Instances)
	if copies < 1 {
		copies = 1
	}
	return planbudget.Size{CPUMilli: cpu, MemoryBytes: memory, DiskBytes: disk}, copies, nil
}

// execQueryer is what the reservation reads need from a transaction or the pool.
type execQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func tierPlan(ctx context.Context, q execQueryer, tier domain.TierType) (config.TierConfig, error) {
	var plan config.TierConfig
	err := q.QueryRowContext(ctx, `SELECT instances, storage_size, memory, cpu FROM tier_configs WHERE tier = $1`, tier).
		Scan(&plan.Instances, &plan.StorageSize, &plan.Memory, &plan.CPU)
	if errors.Is(err, sql.ErrNoRows) {
		return config.GetTierConfig(tier)
	}
	if err != nil {
		return plan, fmt.Errorf("read plan %s: %w", tier, err)
	}
	return plan, nil
}

// recordReservation stores the per-copy size and the copies a project holds.
func recordReservation(ctx context.Context, tx *sql.Tx, projectID string, perCopy planbudget.Size, copies int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE database_instances SET reserved_cpu_milli = $2, reserved_memory_bytes = $3, reserved_disk_bytes = $4, reserved_copies = $5
		WHERE project_id = $1`, projectID, perCopy.CPUMilli, perCopy.MemoryBytes, perCopy.DiskBytes, copies)
	if err != nil {
		return fmt.Errorf("record project reservation: %w", err)
	}
	return nil
}

// releaseSettledReservation drops the reservation of a project that is settled
// without a database: an add that failed and rolled back holds nothing.
func releaseSettledReservation(q execQuerier, inst *domain.DatabaseInstance) error {
	if !inst.NoDatabase || inst.Status != string(domain.StatusActive) {
		return nil
	}
	if _, err := q.Exec(`UPDATE database_instances SET reserved_copies = 0 WHERE project_id = $1 AND reserved_copies <> 0`, inst.ProjectID); err != nil {
		return fmt.Errorf("release project reservation: %w", err)
	}
	return nil
}

// admitInsert admits a new row's reservation, writes the row and records what it holds.
func (s *Store) admitInsert(ctx context.Context, tx *sql.Tx, inst *domain.DatabaseInstance) error {
	perCopy, copies, err := tierReservation(ctx, tx, inst.Tier, inst.StorageSize)
	if err != nil {
		if s.budget.Active(ctx) {
			return err
		}
		log.Printf("WARN: plan budget: %s is left unrecorded while the budget is off: %v", inst.ProjectID, err)
		return insertInstance(tx, inst)
	}
	if inst.NoDatabase {
		copies = 0
	}
	held := planbudget.ReservedDatabase(planbudget.Database{Status: inst.Status, PerCopy: perCopy, Copies: copies})
	if err := s.budget.Admit(ctx, tx, inst.OrgID, func(context.Context) (planbudget.Size, error) { return held, nil }); err != nil {
		return err
	}
	if err := insertInstance(tx, inst); err != nil {
		return err
	}
	return recordReservation(ctx, tx, inst.ProjectID, perCopy, copies)
}

// admitStatusChange admits the growth a status change causes: a paused database
// that resumes takes its CPU and memory back.
func (s *Store) admitStatusChange(ctx context.Context, tx *sql.Tx, orgID string, inst *domain.DatabaseInstance) error {
	return s.budget.Admit(ctx, tx, orgID, func(ctx context.Context) (planbudget.Size, error) {
		row, err := readReservedRow(ctx, tx, inst.ProjectID)
		if err != nil {
			return planbudget.Size{}, err
		}
		after := row
		after.status = inst.Status
		return after.held().Sub(row.held()), nil
	})
}

// ReserveDatabaseSize admits a resize and records the new size, before the
// cluster is touched. A resize down always passes. The size is recorded even
// while the budget is off, so the sum is right the day it comes on; only the
// admission is skipped. See storage.ProjectBudgetStore.
func (s *Store) ReserveDatabaseSize(ctx context.Context, projectID string, next planbudget.Reservation) (planbudget.Reservation, error) {
	var previous planbudget.Reservation
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return previous, fmt.Errorf("begin resize reservation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	orgID, err := projectOrg(tx, projectID)
	if err != nil {
		return previous, err
	}
	err = s.budget.Admit(ctx, tx, orgID, func(ctx context.Context) (planbudget.Size, error) {
		row, err := readReservedRow(ctx, tx, projectID)
		if err != nil {
			return planbudget.Size{}, err
		}
		previous = planbudget.Reservation{PerCopy: row.perCopy, Copies: row.copies}
		after := row
		after.perCopy, after.copies = next.PerCopy, next.Copies
		after.storageSize = ""
		return after.held().Sub(row.held()), nil
	})
	if err != nil {
		return previous, err
	}
	if previous == (planbudget.Reservation{}) {
		row, err := readReservedRow(ctx, tx, projectID)
		if err != nil {
			return previous, err
		}
		previous = planbudget.Reservation{PerCopy: row.perCopy, Copies: row.copies}
	}
	if err := recordReservation(ctx, tx, projectID, next.PerCopy, next.Copies); err != nil {
		return previous, err
	}
	return previous, tx.Commit()
}

// EnsureFunctionsRuntime admits a project's functions runtime the first time it
// is needed and records that it exists. Calling it again costs nothing.
func (s *Store) EnsureFunctionsRuntime(ctx context.Context, projectID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin functions runtime reservation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	orgID, err := projectOrg(tx, projectID)
	if err != nil {
		return err
	}
	err = s.budget.Admit(ctx, tx, orgID, func(ctx context.Context) (planbudget.Size, error) {
		var has bool
		var tier string
		if err := tx.QueryRowContext(ctx, `SELECT has_functions_runtime, tier FROM database_instances WHERE project_id = $1 FOR UPDATE`, projectID).Scan(&has, &tier); err != nil {
			return planbudget.Size{}, fmt.Errorf("read functions runtime: %w", err)
		}
		if has {
			return planbudget.Size{}, nil
		}
		return planbudget.ReservedFunctions(tier), nil
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE database_instances SET has_functions_runtime = TRUE WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("record functions runtime: %w", err)
	}
	return tx.Commit()
}

// BackfillFailure is a database whose size could not be recorded. It stays
// unrecorded, which refuses growth in its org and nowhere else.
type BackfillFailure struct {
	ProjectID string
	Err       error
}

// BackfillReservations records the size of every database that has none yet,
// from the plan as it is configured now, and returns how many it recorded and
// which it could not. It writes the platform's own rows only: no cluster,
// container or workload is read, resized or restarted. It does nothing while
// the budget is off, and is safe to run on every boot.
func (s *Store) BackfillReservations(ctx context.Context) (int, []BackfillFailure, error) {
	if !s.budget.Active(ctx) {
		return 0, nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, tier, COALESCE(storage_size, '') FROM database_instances WHERE reserved_copies = 0 AND NOT no_database`)
	if err != nil {
		return 0, nil, fmt.Errorf("find unrecorded reservations: %w", err)
	}
	type pending struct{ projectID, tier, storage string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.projectID, &p.tier, &p.storage); err != nil {
			rows.Close()
			return 0, nil, err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	var failed []BackfillFailure
	for _, p := range todo {
		if err := s.backfillOne(ctx, p.projectID, domain.TierType(p.tier), p.storage); err != nil {
			failed = append(failed, BackfillFailure{ProjectID: p.projectID, Err: err})
		}
	}
	return len(todo) - len(failed), failed, nil
}

func (s *Store) backfillOne(ctx context.Context, projectID string, tier domain.TierType, storageSize string) error {
	perCopy, copies, err := tierReservation(ctx, s.db, tier, storageSize)
	if err != nil {
		return fmt.Errorf("backfill %s: %w", projectID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE database_instances SET reserved_cpu_milli = $2, reserved_memory_bytes = $3, reserved_disk_bytes = $4, reserved_copies = $5
		WHERE project_id = $1 AND reserved_copies = 0`, projectID, perCopy.CPUMilli, perCopy.MemoryBytes, perCopy.DiskBytes, copies)
	return err
}

// RestoreDatabaseSize puts a recorded reservation back after a resize that
// failed on the cluster. It is not admitted: the database is still running at
// that size, so refusing it would leave the record smaller than the cluster.
func (s *Store) RestoreDatabaseSize(ctx context.Context, projectID string, previous planbudget.Reservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restore of reservation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	orgID, err := projectOrg(tx, projectID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, planbudget.OrgLockSpace, orgID); err != nil {
		return fmt.Errorf("lock org budget: %w", err)
	}
	if err := recordReservation(ctx, tx, projectID, previous.PerCopy, previous.Copies); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkFunctionsRuntimes records that these projects already run functions, so
// their runtimes count from the start. Like the backfill it only writes rows.
func (s *Store) MarkFunctionsRuntimes(ctx context.Context, projectIDs []string) error {
	for _, id := range projectIDs {
		if _, err := s.db.ExecContext(ctx, `UPDATE database_instances SET has_functions_runtime = TRUE WHERE project_id = $1 AND NOT has_functions_runtime`, id); err != nil {
			return fmt.Errorf("mark functions runtime of %s: %w", id, err)
		}
	}
	return nil
}

// admitDiskChange admits a disk that grows. The org lock is taken first, so two
// growths for the last of the disk cannot both read it as free.
func (s *Store) admitDiskChange(ctx context.Context, tx *sql.Tx, projectID, size string) error {
	orgID, err := projectOrg(tx, projectID)
	if err != nil {
		return err
	}
	return s.budget.Admit(ctx, tx, orgID, func(ctx context.Context) (planbudget.Size, error) {
		row, err := readReservedRow(ctx, tx, projectID)
		if err != nil {
			return planbudget.Size{}, err
		}
		after := row
		after.storageSize = size
		return after.held().Sub(row.held()), nil
	})
}

// BudgetActive reports whether the budget admits anything now. See storage.ProjectBudgetStore.
func (s *Store) BudgetActive(ctx context.Context) bool { return s.budget.Active(ctx) }
