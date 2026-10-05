package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// orgProjectLimitLockSpace namespaces the per-organisation advisory lock the
// limit is enforced under. Postgres keeps two-integer advisory locks in a key
// space of their own, separate from the single-bigint locks the schedulers
// lead on, so this cannot collide with them.
const orgProjectLimitLockSpace = 421

// execQuerier is the part of *sql.DB and *sql.Tx the org-limit queries use, so
// the count can run either inside the admitting transaction or on its own.
type execQuerier interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// CreateWithinOrgLimit inserts the project only while its organisation has a
// free slot. See storage.InstanceStore.
//
// Counting and inserting in one transaction is not enough on its own: at READ
// COMMITTED neither transaction sees the other's uncommitted row, so two
// concurrent creates would both count the same free slot and both commit. The
// transaction therefore takes an advisory lock keyed on the organisation
// first. The lock is held to commit (pg_advisory_xact_lock releases at end of
// transaction, on rollback too), so the second create blocks until the first
// row is committed and visible, then counts it. Locking on a value rather than
// a row means an organisation with no row of its own — a self-hosted install,
// a fresh tenant — is serialized just the same.
func (s *Store) CreateWithinOrgLimit(inst *domain.DatabaseInstance, maxProjects int) error {
	if maxProjects <= 0 {
		return s.Create(inst)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin org project limit transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
		orgProjectLimitLockSpace, inst.OrgID); err != nil {
		return fmt.Errorf("lock org project slots: %w", err)
	}
	held, err := countOrgProjectSlots(tx, inst.OrgID)
	if err != nil {
		return err
	}
	if err := storage.CheckOrgProjectSlot(held, maxProjects); err != nil {
		return err
	}
	if err := insertInstance(tx, inst); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateIfStatusWithinOrgLimit writes a project back into a slot-holding
// status only while its organisation has a free slot. See
// storage.InstanceStore. It takes the advisory lock CreateWithinOrgLimit takes,
// so a restore and a create for the last slot are serialized, and the status
// is read after the lock so the count sees whatever the other one committed.
func (s *Store) UpdateIfStatusWithinOrgLimit(inst *domain.DatabaseInstance, expected string, maxProjects int) error {
	if expected == "" {
		return fmt.Errorf("%w: no expected status given for %s", storage.ErrProjectStatusChanged, inst.ProjectID)
	}
	if maxProjects <= 0 || !storage.HoldsOrgProjectSlot(inst.Status) {
		return s.update(inst, expected)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin org project limit transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	orgID, err := projectOrg(tx, inst.ProjectID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
		orgProjectLimitLockSpace, orgID); err != nil {
		return fmt.Errorf("lock org project slots: %w", err)
	}
	if err := admitSlotTakingUpdate(tx, inst.ProjectID, orgID, maxProjects); err != nil {
		return err
	}
	if err := updateInstance(tx, inst, expected); err != nil {
		return err
	}
	return tx.Commit()
}

// projectOrg reads the organisation a stored project belongs to.
func projectOrg(q execQuerier, projectID string) (string, error) {
	var orgID string
	err := q.QueryRow(`SELECT COALESCE(org_id, '') FROM database_instances WHERE project_id = $1`, projectID).Scan(&orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrProjectNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read project org: %w", err)
	}
	return orgID, nil
}

// admitSlotTakingUpdate refuses the write when the row does not hold a slot
// yet and the organisation has none free. A row that already holds one is
// left to the update's own status predicate.
func admitSlotTakingUpdate(q execQuerier, projectID, orgID string, maxProjects int) error {
	var status string
	if err := q.QueryRow(`SELECT status FROM database_instances WHERE project_id = $1`, projectID).Scan(&status); err != nil {
		return fmt.Errorf("read project status: %w", err)
	}
	if storage.HoldsOrgProjectSlot(status) {
		return nil
	}
	held, err := countOrgProjectSlots(q, orgID)
	if err != nil {
		return err
	}
	return storage.CheckOrgProjectSlot(held, maxProjects)
}

// CountOrgProjects reports how many of the organisation's projects hold a
// slot. See storage.InstanceStore.
func (s *Store) CountOrgProjects(orgID string) (int, error) {
	return countOrgProjectSlots(s.db, orgID)
}

// countOrgProjectSlots counts one organisation's slot-holding projects. The
// non-slot statuses are excluded in SQL so the count never loads a row, let
// alone every instance on the platform.
func countOrgProjectSlots(q execQuerier, orgID string) (int, error) {
	var held int
	err := q.QueryRow(`
		SELECT count(*) FROM database_instances
		WHERE org_id = $1 AND status <> ALL($2)`,
		orgID, pq.Array(storage.NonSlotStatuses())).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("count org projects: %w", err)
	}
	return held, nil
}
