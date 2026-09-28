package postgres

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// RecordDatabaseChoices writes a new database's create-time choices. See
// storage.ProjectDatabaseStore.
func (s *Store) RecordDatabaseChoices(inst *domain.DatabaseInstance, expected string) error {
	if expected == "" {
		return fmt.Errorf("%w: no expected status given for %s", storage.ErrProjectStatusChanged, inst.ProjectID)
	}
	parameters, err := encodeParameters(inst.Parameters)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE database_instances
		SET documentdb = $2, storage_class = $3, parameters = $4, storage_size = $5, updated_at = now()
		WHERE project_id = $1 AND no_database AND status = $6 AND status <> ALL($7)`,
		inst.ProjectID, inst.DocumentDB, inst.StorageClass, parameters, inst.StorageSize,
		expected, pq.Array(deletionStatuses))
	if err != nil {
		return fmt.Errorf("record database choices: %w", err)
	}
	return s.explainNoDatabaseWrite(res, inst.ProjectID, expected)
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
