package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// previewLockSpace serialises preview creation across replicas, so the caps hold.
const previewLockSpace = 660

var _ PreviewStore = (*PostgresStore)(nil)

const previewColumns = `id, site_id, project_id, source, ref, from_fork, hostname, status, commit_sha, COALESCE(deployed_sha, ''),
	COALESCE(failure_reason, ''), file_count, total_bytes, cors_claimed, created_by, created_at, updated_at, deployed_at`

func scanPreview(row rowScanner) (*Preview, error) {
	preview := &Preview{}
	var deployed sql.NullTime
	err := row.Scan(&preview.ID, &preview.SiteID, &preview.ProjectID, &preview.Source, &preview.Ref, &preview.FromFork, &preview.Hostname, &preview.Status,
		&preview.CommitSHA, &preview.DeployedSHA, &preview.FailureReason, &preview.FileCount, &preview.TotalBytes, &preview.CorsClaimed,
		&preview.CreatedBy, &preview.CreatedAt, &preview.UpdatedAt, &deployed)
	if deployed.Valid {
		preview.DeployedAt = &deployed.Time
	}
	return preview, err
}

func (s *PostgresStore) OpenPreview(ctx context.Context, preview *Preview, maxPerProject, maxInstall int) (*Preview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin preview open: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, previewLockSpace); err != nil {
		return nil, fmt.Errorf("lock preview counts: %w", err)
	}
	held, err := scanPreview(tx.QueryRowContext(ctx, `SELECT `+previewColumns+` FROM site_previews
		WHERE site_id = $1 AND project_id = $2 AND source = $3 AND ref = $4`, preview.SiteID, preview.ProjectID, preview.Source, preview.Ref))
	switch {
	case err == nil:
		return s.reopenPreview(ctx, tx, held, preview)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read the preview: %w", err)
	}
	var inProject, inInstall int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE project_id = $1), count(*) FROM site_previews`,
		preview.ProjectID).Scan(&inProject, &inInstall); err != nil {
		return nil, fmt.Errorf("count previews: %w", err)
	}
	if inProject >= maxPerProject {
		return nil, ErrPreviewLimitReached
	}
	if inInstall >= maxInstall {
		return nil, ErrPreviewInstallFull
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_previews (id, site_id, project_id, source, ref, from_fork, hostname, status, commit_sha,
		created_by, created_at, updated_at)
		SELECT $1, id, project_id, $4, $5, $12, $6, $7, $8, $9, $10, $11 FROM sites WHERE id = $2 AND project_id = $3`,
		preview.ID, preview.SiteID, preview.ProjectID, preview.Source, preview.Ref, preview.Hostname, preview.Status, preview.CommitSHA,
		preview.CreatedBy, preview.CreatedAt, preview.UpdatedAt, preview.FromFork)
	if err != nil {
		return nil, fmt.Errorf("create the preview: %w", err)
	}
	if err := touchedOne(result, ErrSiteNotFound); err != nil {
		return nil, err
	}
	created := *preview
	return &created, tx.Commit()
}

func (s *PostgresStore) reopenPreview(ctx context.Context, tx *sql.Tx, held, asked *Preview) (*Preview, error) {
	if held.Status == PreviewRemoving {
		return nil, ErrPreviewRemoving
	}
	// A removal marked since the read wins: REMOVING is never undone.
	result, err := tx.ExecContext(ctx, `UPDATE site_previews SET status = $2, commit_sha = $3, updated_at = $4
		WHERE id = $1 AND status <> 'REMOVING'`, held.ID, PreviewBuilding, asked.CommitSHA, asked.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("reopen the preview: %w", err)
	}
	if err := touchedOne(result, ErrPreviewRemoving); err != nil {
		return nil, err
	}
	held.Status, held.CommitSHA, held.UpdatedAt = PreviewBuilding, asked.CommitSHA, asked.UpdatedAt
	return held, tx.Commit()
}

func (s *PostgresStore) GetPreview(ctx context.Context, projectID, siteID, id string) (*Preview, error) {
	preview, err := scanPreview(s.db.QueryRowContext(ctx, `SELECT `+previewColumns+` FROM site_previews
		WHERE id = $1 AND project_id = $2 AND site_id = $3`, id, projectID, siteID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the preview: %w", err)
	}
	return preview, nil
}

func (s *PostgresStore) FindPreview(ctx context.Context, projectID, siteID, source, ref string) (*Preview, error) {
	preview, err := scanPreview(s.db.QueryRowContext(ctx, `SELECT `+previewColumns+` FROM site_previews
		WHERE project_id = $1 AND site_id = $2 AND source = $3 AND ref = $4`, projectID, siteID, source, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the preview: %w", err)
	}
	return preview, nil
}

func (s *PostgresStore) listPreviews(ctx context.Context, query string, args ...any) ([]*Preview, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list previews: %w", err)
	}
	defer rows.Close()
	out := []*Preview{}
	for rows.Next() {
		preview, err := scanPreview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, preview)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListPreviews(ctx context.Context, projectID, siteID string) ([]*Preview, error) {
	return s.listPreviews(ctx, `SELECT `+previewColumns+` FROM site_previews WHERE project_id = $1 AND site_id = $2
		ORDER BY created_at`, projectID, siteID)
}

func (s *PostgresStore) CountPreviews(ctx context.Context, projectID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM site_previews WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count the project's previews: %w", err)
	}
	return count, nil
}

// ClaimPreview tells a missing preview from a busy one with a second read, as ClaimDeploy does for sites.
func (s *PostgresStore) ClaimPreview(ctx context.Context, projectID, id, lease string, until time.Time, removing bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_previews SET lease_token = $3, lease_until = $4
		WHERE id = $1 AND project_id = $2 AND (status <> 'REMOVING' OR $5)
		  AND (lease_until IS NULL OR lease_until < NOW())`, id, projectID, lease, until, removing)
	if err != nil {
		return fmt.Errorf("claim the preview: %w", err)
	}
	touched, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim the preview: %w", err)
	}
	if touched == 1 {
		return nil
	}
	var status string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM site_previews WHERE id = $1 AND project_id = $2`, id, projectID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status == PreviewRemoving && !removing) {
		return ErrPreviewNotFound
	}
	if err != nil {
		return fmt.Errorf("read the preview: %w", err)
	}
	return ErrSiteBusy
}

func (s *PostgresStore) ReleasePreview(ctx context.Context, projectID, id, lease string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE site_previews SET lease_token = NULL, lease_until = NULL
		WHERE id = $1 AND project_id = $2 AND lease_token = $3`, id, projectID, lease)
	if err != nil {
		return fmt.Errorf("release the preview: %w", err)
	}
	return nil
}

// FinishPreview never takes a preview out of REMOVING: a removal asked meanwhile wins.
func (s *PostgresStore) FinishPreview(ctx context.Context, preview *Preview, lease string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_previews SET
		status = CASE WHEN status = 'REMOVING' THEN status ELSE $4 END, deployed_sha = NULLIF($5, ''), failure_reason = NULLIF($6, ''),
		file_count = $7, total_bytes = $8, cors_claimed = $9, updated_at = $10, deployed_at = $11
		WHERE id = $1 AND project_id = $2 AND lease_token = $3 AND lease_until > NOW()`,
		preview.ID, preview.ProjectID, lease, preview.Status, preview.DeployedSHA, preview.FailureReason, preview.FileCount,
		preview.TotalBytes, preview.CorsClaimed, preview.UpdatedAt, preview.DeployedAt)
	if err != nil {
		return fmt.Errorf("record the preview: %w", err)
	}
	return touchedOne(result, ErrLeaseLost)
}

func (s *PostgresStore) MarkPreviewRemoving(ctx context.Context, projectID, id string, idleBefore time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_previews SET status = 'REMOVING' WHERE id = $1 AND project_id = $2
		AND ($3::timestamptz IS NULL OR status = 'REMOVING' OR updated_at < $3)`, id, projectID, nullTime(idleBefore))
	if err != nil {
		return fmt.Errorf("mark the preview for removal: %w", err)
	}
	return touchedOne(result, ErrPreviewNotFound)
}

func (s *PostgresStore) DeletePreview(ctx context.Context, projectID, id, lease string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM site_previews WHERE id = $1 AND project_id = $2 AND lease_token = $3
		AND lease_until > NOW()`, id, projectID, lease)
	if err != nil {
		return fmt.Errorf("delete the preview: %w", err)
	}
	return touchedOne(result, ErrLeaseLost)
}

func (s *PostgresStore) DuePreviews(ctx context.Context, idleBefore time.Time, limit int) ([]*Preview, error) {
	// Removals waiting for a retry come first, so idle previews never crowd them out.
	return s.listPreviews(ctx, `SELECT `+previewColumns+` FROM site_previews
		WHERE status = 'REMOVING' OR updated_at < $1 ORDER BY (status = 'REMOVING') DESC, updated_at LIMIT $2`, idleBefore, limit)
}

// touchedOne answers none when the statement changed no row.
func touchedOne(result sql.Result, none error) error {
	touched, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count the rows changed: %w", err)
	}
	if touched == 0 {
		return none
	}
	return nil
}

// nullTime is NULL for the zero time.
func nullTime(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: !at.IsZero()} }
