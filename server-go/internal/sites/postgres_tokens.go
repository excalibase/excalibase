package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// siteTokenLockSpace keeps the per-site token count apart from other advisory locks.
const siteTokenLockSpace = 620

const tokenColumns = `id, site_id, project_id, name, token_prefix, token_hash, created_by, created_at, expires_at, last_used_at`

func scanDeployToken(row rowScanner) (*DeployToken, error) {
	token := &DeployToken{}
	var lastUsed sql.NullTime
	if err := row.Scan(&token.ID, &token.SiteID, &token.ProjectID, &token.Name, &token.Prefix, &token.Hash,
		&token.CreatedBy, &token.CreatedAt, &token.ExpiresAt, &lastUsed); err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		used := lastUsed.Time
		token.LastUsedAt = &used
	}
	return token, nil
}

// CreateDeployToken counts the site's tokens under a per-site lock, so two creates cannot both take the last slot.
func (s *PostgresStore) CreateDeployToken(ctx context.Context, token *DeployToken, maxPerSite int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin deploy token create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, siteTokenLockSpace, token.SiteID); err != nil {
		return fmt.Errorf("lock site token count: %w", err)
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM site_deploy_tokens WHERE site_id = $1`, token.SiteID).Scan(&held); err != nil {
		return fmt.Errorf("count site deploy tokens: %w", err)
	}
	if held >= maxPerSite {
		return ErrDeployTokenLimitReached
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_deploy_tokens (id, site_id, project_id, name, token_prefix, token_hash, created_by, created_at, expires_at)
		SELECT $1, id, project_id, $4, $5, $6, $7, $8, $9 FROM sites WHERE id = $2 AND project_id = $3`,
		token.ID, token.SiteID, token.ProjectID, token.Name, token.Prefix, token.Hash, token.CreatedBy, token.CreatedAt, token.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create site deploy token: %w", err)
	}
	if created, err := result.RowsAffected(); err != nil || created == 0 {
		return errors.Join(ErrSiteNotFound, err)
	}
	return tx.Commit()
}

func (s *PostgresStore) ListDeployTokens(ctx context.Context, projectID, siteID string) ([]*DeployToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM site_deploy_tokens
		WHERE project_id = $1 AND site_id = $2 ORDER BY created_at, id`, projectID, siteID)
	if err != nil {
		return nil, fmt.Errorf("list site deploy tokens: %w", err)
	}
	defer rows.Close()
	out := []*DeployToken{}
	for rows.Next() {
		token, err := scanDeployToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan site deploy token: %w", err)
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteDeployToken(ctx context.Context, projectID, siteID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM site_deploy_tokens WHERE project_id = $1 AND site_id = $2 AND id = $3`, projectID, siteID, id)
	if err != nil {
		return fmt.Errorf("revoke site deploy token: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke site deploy token: %w", err)
	}
	if deleted == 0 {
		return ErrDeployTokenNotFound
	}
	return nil
}

func (s *PostgresStore) FindDeployToken(ctx context.Context, hash string) (*DeployToken, error) {
	token, err := scanDeployToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM site_deploy_tokens WHERE token_hash = $1`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read site deploy token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) TouchDeployToken(ctx context.Context, id string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE site_deploy_tokens SET last_used_at = $2 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("record site deploy token use: %w", err)
	}
	return nil
}
