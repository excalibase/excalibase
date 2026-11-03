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

// Advisory lock spaces: the per-project hostname count, and the install's
// count of hostnames on the zone (one key).
const (
	hostnameLimitLockSpace = 614
	hostnameZoneLockSpace  = 6140
)

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

var _ HostnameStore = (*PostgresStore)(nil)

const hostnameColumns = `id, site_id, project_id, hostname, validation, status, COALESCE(cloudflare_id, ''), hostname_status,
	certificate_status, records, errors, created_by, created_at, updated_at, checked_at, verification_token, verified_at, inactive_since, withdrawn_at`

func scanHostname(row rowScanner) (*CustomHostname, error) {
	hostname := &CustomHostname{}
	var records, failures []byte
	var verifiedAt, inactiveSince, withdrawnAt sql.NullTime
	err := row.Scan(&hostname.ID, &hostname.SiteID, &hostname.ProjectID, &hostname.Hostname, &hostname.Validation, &hostname.Status,
		&hostname.CloudflareID, &hostname.HostnameStatus, &hostname.CertificateStatus, &records, &failures,
		&hostname.CreatedBy, &hostname.CreatedAt, &hostname.UpdatedAt, &hostname.CheckedAt, &hostname.VerificationToken,
		&verifiedAt, &inactiveSince, &withdrawnAt)
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
	hostname.WithdrawnAt = timeOrNil(withdrawnAt)
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

// CreateHostname counts the project's claims under a per-project lock, so two
// adds cannot both take the last slot. Only a proven row holds a name across
// projects; the one-claim-per-project rule is the table's.
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
	var proven bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM site_hostnames WHERE hostname = $1 AND verified_at IS NOT NULL)`,
		hostname.Hostname).Scan(&proven); err != nil {
		return fmt.Errorf("read who holds the custom domain: %w", err)
	}
	if proven {
		return ErrHostnameTaken
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_hostnames
		(id, site_id, project_id, hostname, validation, status, records, errors, created_by, created_at, updated_at,
		 verification_token, inactive_since)
		SELECT $1, id, project_id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13 FROM sites WHERE id = $2 AND project_id = $3`,
		hostname.ID, hostname.SiteID, hostname.ProjectID, hostname.Hostname, hostname.Validation, hostname.Status,
		records, failures, hostname.CreatedBy, hostname.CreatedAt, hostname.UpdatedAt, hostname.VerificationToken, hostname.InactiveSince)
	if isUniqueViolation(err) {
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

const getHostnameSQL = `SELECT id, site_id, project_id, hostname, validation, status, COALESCE(cloudflare_id, ''), hostname_status,
	certificate_status, records, errors, created_by, created_at, updated_at, checked_at, verification_token, verified_at, inactive_since, withdrawn_at
		FROM site_hostnames WHERE project_id = $1 AND site_id = $2 AND id = $3`

func (s *PostgresStore) GetHostname(ctx context.Context, projectID, siteID, id string) (*CustomHostname, error) {
	hostname, err := scanHostname(s.db.QueryRowContext(ctx, getHostnameSQL, projectID, siteID, id))
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

// DueHostnames leaves out projects inside their deletion grace period: their sites must stay offline.
func (s *PostgresStore) DueHostnames(ctx context.Context, activeCheckedBefore time.Time, limit int) ([]*CustomHostname, error) {
	return s.queryHostnames(ctx, `SELECT `+hostnameColumns+` FROM site_hostnames h
		WHERE (h.status <> 'ACTIVE' OR h.checked_at < $1) AND NOT EXISTS (
			SELECT 1 FROM database_instances i WHERE i.project_id = h.project_id AND i.status IN ('PENDING_DELETION', 'DELETING', 'BACKUPS_PENDING_DELETE'))
		ORDER BY h.checked_at, h.id LIMIT $2`, activeCheckedBefore, limit)
}

// VerifyHostname counts the zone's proven hostnames under one install-wide
// lock, so two proofs cannot both take the last place; the partial unique
// index keeps one proven row per hostname.
func (s *PostgresStore) VerifyHostname(ctx context.Context, hostname *CustomHostname, maxOnZone int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin custom domain proof: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, hostnameZoneLockSpace); err != nil {
		return fmt.Errorf("lock the zone's custom domain count: %w", err)
	}
	var proven int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM site_hostnames WHERE verified_at IS NOT NULL`).Scan(&proven); err != nil {
		return fmt.Errorf("count the zone's custom domains: %w", err)
	}
	if proven >= maxOnZone {
		return ErrCustomDomainsFull
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_hostnames SET verified_at = $4, updated_at = $5
		WHERE project_id = $1 AND site_id = $2 AND id = $3`,
		hostname.ProjectID, hostname.SiteID, hostname.ID, hostname.VerifiedAt, hostname.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrHostnameTaken
	}
	if err != nil {
		return fmt.Errorf("record the custom domain's proof: %w", err)
	}
	if updated, err := result.RowsAffected(); err != nil || updated == 0 {
		return errors.Join(ErrHostnameNotFound, err)
	}
	return tx.Commit()
}

func (s *PostgresStore) CountVerifiedHostnames(ctx context.Context) (int, error) {
	var proven int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM site_hostnames WHERE verified_at IS NOT NULL`).Scan(&proven); err != nil {
		return 0, fmt.Errorf("count the zone's custom domains: %w", err)
	}
	return proven, nil
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
	err = s.execHostname(ctx, "update custom domain", `UPDATE site_hostnames SET cloudflare_id = NULLIF($4, ''), status = $5,
		hostname_status = $6, certificate_status = $7, records = $8, errors = $9, updated_at = $10, checked_at = $11,
		verified_at = $12, inactive_since = $13, withdrawn_at = $14
		WHERE project_id = $1 AND site_id = $2 AND id = $3`,
		hostname.ProjectID, hostname.SiteID, hostname.ID, hostname.CloudflareID, hostname.Status, hostname.HostnameStatus,
		hostname.CertificateStatus, records, failures, hostname.UpdatedAt, hostname.CheckedAt, hostname.VerifiedAt, hostname.InactiveSince, hostname.WithdrawnAt)
	if isUniqueViolation(err) {
		return ErrHostnameTaken
	}
	return err
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
