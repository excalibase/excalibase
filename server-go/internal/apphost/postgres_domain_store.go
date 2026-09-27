package apphost

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// domainLockSpace serializes adds per app so the per-app cap cannot be raced.
const domainLockSpace = 385

var domainStatuses = map[string]bool{
	DomainPending: true, DomainIssuing: true, DomainActive: true, DomainIssueFailed: true, DomainDetached: true,
}

type PostgresDomainStore struct {
	db *sql.DB
}

func NewPostgresDomainStore(db *sql.DB) *PostgresDomainStore { return &PostgresDomainStore{db: db} }

var _ DomainStore = (*PostgresDomainStore)(nil)

const domainColumns = `id, project_id, app_id, hostname, status, failure_reason, consecutive_failures, verified_at, last_checked_at, created_at`

func (s *PostgresDomainStore) Add(d *Domain) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin domain add: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`, domainLockSpace, d.AppID); err != nil {
		return fmt.Errorf("lock app domains: %w", err)
	}
	var held int
	if err := tx.QueryRow(`SELECT count(*) FROM app_domains WHERE project_id = $1 AND app_id = $2`,
		d.ProjectID, d.AppID).Scan(&held); err != nil {
		return fmt.Errorf("count app domains: %w", err)
	}
	if held >= MaxDomainsPerApp {
		return ErrDomainLimit
	}
	_, err = tx.Exec(`INSERT INTO app_domains (id, project_id, app_id, hostname, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, d.ID, d.ProjectID, d.AppID, d.Hostname, d.Status, d.CreatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation {
		return ErrDomainExists
	}
	if err != nil {
		return fmt.Errorf("add domain: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresDomainStore) List(projectID, appID string) ([]*Domain, error) {
	return s.query(`SELECT `+domainColumns+` FROM app_domains WHERE project_id = $1 AND app_id = $2 ORDER BY created_at`, projectID, appID)
}

func (s *PostgresDomainStore) ListRoutable() ([]*Domain, error) {
	return s.query(`SELECT `+domainColumns+` FROM app_domains WHERE status IN ($1, $2, $3) ORDER BY created_at`,
		DomainIssuing, DomainActive, DomainIssueFailed)
}

func (s *PostgresDomainStore) Get(projectID, appID, id string) (*Domain, error) {
	list, err := s.query(`SELECT `+domainColumns+` FROM app_domains WHERE id = $1 AND project_id = $2 AND app_id = $3`, id, projectID, appID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

func (s *PostgresDomainStore) Verify(projectID, appID, id string, at time.Time) error {
	res, err := s.db.Exec(`UPDATE app_domains SET status = $4, failure_reason = NULL, consecutive_failures = 0,
		verified_at = $5, last_checked_at = $5 WHERE id = $1 AND project_id = $2 AND app_id = $3`,
		id, projectID, appID, DomainIssuing, at)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation {
		return ErrDomainClaimed
	}
	return affectedOne(res, err, "verify domain")
}

func (s *PostgresDomainStore) SetStatus(id, status, failureReason string, failures int, checkedAt time.Time) error {
	if !domainStatuses[status] {
		return fmt.Errorf("unknown domain status: %q", status)
	}
	res, err := s.db.Exec(`UPDATE app_domains SET status = $2, failure_reason = NULLIF($3, ''),
		consecutive_failures = $4, last_checked_at = $5 WHERE id = $1`, id, status, failureReason, failures, checkedAt)
	return affectedOne(res, err, "record domain status")
}

func (s *PostgresDomainStore) Delete(projectID, appID, id string) error {
	res, err := s.db.Exec(`DELETE FROM app_domains WHERE id = $1 AND project_id = $2 AND app_id = $3`, id, projectID, appID)
	return affectedOne(res, err, "delete domain")
}

func affectedOne(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

func (s *PostgresDomainStore) query(q string, args ...any) ([]*Domain, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer rows.Close()
	out := make([]*Domain, 0)
	for rows.Next() {
		var d Domain
		var reason sql.NullString
		var verified, checked sql.NullTime
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.AppID, &d.Hostname, &d.Status, &reason,
			&d.ConsecutiveFailures, &verified, &checked, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		d.FailureReason = reason.String
		if verified.Valid {
			d.VerifiedAt = &verified.Time
		}
		if checked.Valid {
			d.LastCheckedAt = &checked.Time
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}
