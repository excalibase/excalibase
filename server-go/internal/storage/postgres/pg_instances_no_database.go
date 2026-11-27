package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// RecordDatabaseChoices writes a new database's create-time choices. See
// storage.ProjectDatabaseStore. The database is admitted against the org's
// budget here, at the start of the add, and the reservation is recorded in the
// same transaction, so two adds racing for the last of the budget cannot both
// pass: the second reads the first's reservation. A failed add gives it back
// when the project settles without a database again.
func (s *Store) RecordDatabaseChoices(inst *domain.DatabaseInstance, expected string) error {
	if expected == "" {
		return fmt.Errorf("%w: no expected status given for %s", storage.ErrProjectStatusChanged, inst.ProjectID)
	}
	parameters, err := encodeParameters(inst.Parameters)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database choices: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	perCopy, copies, err := s.admitAddedDatabase(ctx, tx, inst, expected)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE database_instances
		SET documentdb = $2, storage_class = $3, parameters = $4, storage_size = $5, updated_at = now(),
		    reserved_cpu_milli = $8, reserved_memory_bytes = $9, reserved_disk_bytes = $10, reserved_copies = $11
		WHERE project_id = $1 AND no_database AND status = $6 AND status <> ALL($7)`,
		inst.ProjectID, inst.DocumentDB, inst.StorageClass, parameters, inst.StorageSize,
		expected, pq.Array(deletionStatuses), perCopy.CPUMilli, perCopy.MemoryBytes, perCopy.DiskBytes, copies)
	if err != nil {
		return fmt.Errorf("record database choices: %w", err)
	}
	if err := s.explainNoDatabaseWrite(res, inst.ProjectID, expected); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkDatabaseAdded records that the project now has its database. See
// storage.ProjectDatabaseStore.
func (s *Store) MarkDatabaseAdded(projectID string) error {
	res, err := s.db.Exec(`
		UPDATE database_instances SET no_database = FALSE, updated_at = now()
		WHERE project_id = $1 AND no_database AND status <> ALL($2)`,
		projectID, pq.Array(deletionStatuses))
	if err != nil {
		return fmt.Errorf("mark database added: %w", err)
	}
	return s.explainNoDatabaseWrite(res, projectID, "")
}

type rowsAffecter interface{ RowsAffected() (int64, error) }

func (s *Store) explainNoDatabaseWrite(res rowsAffecter, projectID, expected string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var noDatabase bool
	if err := s.db.QueryRow(`SELECT no_database FROM database_instances WHERE project_id = $1`, projectID).Scan(&noDatabase); err == nil && !noDatabase {
		return fmt.Errorf("%w: %s", storage.ErrProjectHasDatabase, projectID)
	}
	return s.explainRefusedUpdate(projectID, expected)
}

// admitAddedDatabase admits the database an add is about to create and returns
// the size it reserves. A project that already holds a reservation (a retry of
// the same add) is not charged twice.
func (s *Store) admitAddedDatabase(ctx context.Context, tx *sql.Tx, inst *domain.DatabaseInstance, status string) (planbudget.Size, int64, error) {
	orgID, err := projectOrg(tx, inst.ProjectID)
	if err != nil {
		return planbudget.Size{}, 0, err
	}
	perCopy, copies, err := tierReservation(ctx, tx, inst.Tier, inst.StorageSize)
	if err != nil {
		return planbudget.Size{}, 0, err
	}
	adding := planbudget.ReservedDatabase(planbudget.Database{Status: status, PerCopy: perCopy, Copies: copies})
	err = s.budget.Admit(ctx, tx, orgID, func(ctx context.Context) (planbudget.Size, error) {
		row, err := readReservedRow(ctx, tx, inst.ProjectID)
		if err != nil || row.copies > 0 {
			return planbudget.Size{}, err
		}
		return adding, nil
	})
	return perCopy, copies, err
}
