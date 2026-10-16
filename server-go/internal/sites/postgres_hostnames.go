package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// hostnameLimitLockSpace keeps the per-project hostname count apart from other advisory locks.
const hostnameLimitLockSpace = 614

var _ HostnameStore = (*PostgresStore)(nil)

const hostnameColumns = `id, site_id, project_id, hostname, validation, status, COALESCE(cloudflare_id, ''), hostname_status,
	certificate_status, records, errors, created_by, created_at, updated_at, checked_at, verification_token, verified_at, inactive_since`

func scanHostname(row rowScanner) (*CustomHostname, error) {
	hostname := &CustomHostname{}
	var records, failures []byte
	var verifiedAt, inactiveSince sql.NullTime
	err := row.Scan(&hostname.ID, &hostname.SiteID, &hostname.ProjectID, &hostname.Hostname, &hostname.Validation, &hostname.Status,
		&hostname.CloudflareID, &hostname.HostnameStatus, &hostname.CertificateStatus, &records, &failures,
		&hostname.CreatedBy, &hostname.CreatedAt, &hostname.UpdatedAt, &hostname.CheckedAt, &hostname.VerificationToken,
		&verifiedAt, &inactiveSince)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(records, &hostname.Records); err != nil {
		return nil, fmt.Errorf("decode custom domain records: %w", err)
	}
	if err := json.Unmarshal(failures, &hostname.Errors); err != nil {
		return nil, fmt.Errorf("decode custom domain errors: %w", err)
	}
	hostname.VerifiedAt = timeOrNil(verifiedAt)
	hostname.InactiveSince = timeOrNil(inactiveSince)
	return hostname, nil
}

func timeOrNil(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	held := value.Time
	return &held
}

func encodeHostnameState(hostname *CustomHostname) ([]byte, []byte, error) {
	records, err := json.Marshal(nonNil(hostname.Records))
	if err != nil {
		return nil, nil, fmt.Errorf("encode custom domain records: %w", err)
	}
	failures, err := json.Marshal(nonNil(hostname.Errors))
	if err != nil {
		return nil, nil, fmt.Errorf("encode custom domain errors: %w", err)
	}
	return records, failures, nil
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// CreateHostname counts the project's hostnames under a per-project lock, so two adds cannot both take the last slot.
func (s *PostgresStore) CreateHostname(ctx context.Context, hostname *CustomHostname, maxPerProject int) error {
	records, failures, err := encodeHostnameState(hostname)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin custom domain create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, hostnameLimitLockSpace, hostname.ProjectID); err != nil {
		return fmt.Errorf("lock project custom domain count: %w", err)
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM site_hostnames WHERE project_id = $1`, hostname.ProjectID).Scan(&held); err != nil {
		return fmt.Errorf("count project custom domains: %w", err)
	}
	if held >= maxPerProject {
		return ErrHostnameLimitReached
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_hostnames
		(id, site_id, project_id, hostname, validation, status, records, errors, created_by, created_at, updated_at,
		 verification_token, inactive_since)
		SELECT $1, id, project_id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13 FROM sites WHERE id = $2 AND project_id = $3`,
		hostname.ID, hostname.SiteID, hostname.ProjectID, hostname.Hostname, hostname.Validation, hostname.Status,
		records, failures, hostname.CreatedBy, hostname.CreatedAt, hostname.UpdatedAt, hostname.VerificationToken, hostname.InactiveSince)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return ErrHostnameTaken
	}
	if err != nil {
		return fmt.Errorf("create custom domain: %w", err)
	}
	if created, err := result.RowsAffected(); err != nil || created == 0 {
		return errors.Join(ErrSiteNotFound, err)
	}
	return tx.Commit()
}

func (s *PostgresStore) GetHostname(ctx context.Context, projectID, siteID, id string) (*CustomHostname, error) {
	hostname, err := scanHostname(s.db.QueryRowContext(ctx, `SELECT `+hostnameColumns+`
		FROM site_hostnames WHERE project_id = $1 AND site_id = $2 AND id = $3`, projectID, siteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read custom domain: %w", err)
	}
	return hostname, nil
}

func (s *PostgresStore) ListHostnames(ctx context.Context, projectID, siteID string) ([]*CustomHostname, error) {
	return s.queryHostnames(ctx, `SELECT `+hostnameColumns+` FROM site_hostnames
		WHERE project_id = $1 AND site_id = $2 ORDER BY hostname`, projectID, siteID)
}

func (s *PostgresStore) ListProjectHostnames(ctx context.Context, projectID string) ([]*CustomHostname, error) {
	return s.queryHostnames(ctx, `SELECT `+hostnameColumns+` FROM site_hostnames
		WHERE project_id = $1 ORDER BY hostname`, projectID)
}

// PendingHostnames leaves out projects inside their deletion grace period: their sites must stay offline.
func (s *PostgresStore) PendingHostnames(ctx context.Context, limit int) ([]*CustomHostname, error) {
	return s.queryHostnames(ctx, `SELECT `+hostnameColumns+` FROM site_hostnames h
		WHERE h.status <> 'ACTIVE' AND NOT EXISTS (
			SELECT 1 FROM database_instances i WHERE i.project_id = h.project_id AND i.status IN ('PENDING_DELETION', 'DELETING', 'BACKUPS_PENDING_DELETE'))
		ORDER BY h.checked_at, h.id LIMIT $1`, limit)
}

func (s *PostgresStore) queryHostnames(ctx context.Context, query string, args ...any) ([]*CustomHostname, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list custom domains: %w", err)
	}
	defer rows.Close()
	out := []*CustomHostname{}
	for rows.Next() {
		hostname, err := scanHostname(rows)
		if err != nil {
			return nil, fmt.Errorf("scan custom domain: %w", err)
		}
		out = append(out, hostname)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateHostname(ctx context.Context, hostname *CustomHostname) error {
	records, failures, err := encodeHostnameState(hostname)
	if err != nil {
		return err
	}
	return s.execHostname(ctx, "update custom domain", `UPDATE site_hostnames SET cloudflare_id = NULLIF($4, ''), status = $5,
		hostname_status = $6, certificate_status = $7, records = $8, errors = $9, updated_at = $10, checked_at = $11,
		verified_at = $12, inactive_since = $13
		WHERE project_id = $1 AND site_id = $2 AND id = $3`,
		hostname.ProjectID, hostname.SiteID, hostname.ID, hostname.CloudflareID, hostname.Status, hostname.HostnameStatus,
		hostname.CertificateStatus, records, failures, hostname.UpdatedAt, hostname.CheckedAt, hostname.VerifiedAt, hostname.InactiveSince)
}

func (s *PostgresStore) DeleteHostname(ctx context.Context, projectID, siteID, id string) error {
	return s.execHostname(ctx, "delete custom domain", `DELETE FROM site_hostnames
		WHERE project_id = $1 AND site_id = $2 AND id = $3`, projectID, siteID, id)
}

func (s *PostgresStore) execHostname(ctx context.Context, what, query string, args ...any) error {
	err := s.execOne(ctx, what, query, args...)
	if errors.Is(err, ErrSiteNotFound) {
		return ErrHostnameNotFound
	}
	return err
}
