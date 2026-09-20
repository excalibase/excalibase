package postgres

import (
	"database/sql"
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

// CountOrgProjects reports how many of the organisation's projects hold a
// slot. See storage.InstanceStore.
func (s *Store) CountOrgProjects(orgID string) (int, error) {
	return countOrgProjectSlots(s.db, orgID)
}

// countOrgProjectSlots counts one organisation's slot-holding projects. The
// deletion statuses are excluded in SQL so the count never loads a row, let
// alone every instance on the platform.
func countOrgProjectSlots(q execQuerier, orgID string) (int, error) {
	var held int
	err := q.QueryRow(`
		SELECT count(*) FROM database_instances
		WHERE org_id = $1 AND status <> ALL($2)`,
		orgID, pq.Array(deletionStatuses)).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("count org projects: %w", err)
	}
	return held, nil
}
