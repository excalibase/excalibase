package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func (s *PostgresStore) CreateDeploy(ctx context.Context, deploy *Deploy) error {
	manifest, err := json.Marshal(deploy.Manifest)
	if err != nil {
		return fmt.Errorf("encode deploy manifest: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO site_deploys
		(id, site_id, project_id, status, manifest, file_count, total_bytes, spa_fallback, label, created_by, created_at)
		SELECT $1, id, project_id, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11 FROM sites WHERE id = $2 AND project_id = $3`,
		deploy.ID, deploy.SiteID, deploy.ProjectID, deploy.Status, manifest, deploy.FileCount, deploy.TotalBytes,
		deploy.SPAFallback, deploy.Label, deploy.CreatedBy, deploy.CreatedAt)
	if err != nil {
		return fmt.Errorf("create site deploy: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create site deploy: %w", err)
	}
	if created == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// FinishDeploy writes the outcome and, for a success, the live pointer in one transaction.
func (s *PostgresStore) FinishDeploy(ctx context.Context, deploy *Deploy) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin deploy finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE site_deploys SET status = $4, failure_reason = NULLIF($5, ''), finished_at = $6
		WHERE id = $1 AND site_id = $2 AND project_id = $3 AND status = 'PENDING'`,
		deploy.ID, deploy.SiteID, deploy.ProjectID, deploy.Status, deploy.FailureReason, deploy.FinishedAt)
	if err != nil {
		return fmt.Errorf("finish site deploy: %w", err)
	}
	if touched, err := result.RowsAffected(); err != nil || touched == 0 {
		return errors.Join(ErrDeployNotFound, err)
	}
	if deploy.Status == DeploySucceeded {
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET live_deploy_id = $3, updated_at = NOW() WHERE id = $1 AND project_id = $2`,
			deploy.SiteID, deploy.ProjectID, deploy.ID); err != nil {
			return fmt.Errorf("make deploy live: %w", err)
		}
	}
	return tx.Commit()
}

func scanDeploy(row rowScanner) (*Deploy, error) {
	deploy := &Deploy{}
	var manifest []byte
	var finishedAt sql.NullTime
	err := row.Scan(&deploy.ID, &deploy.SiteID, &deploy.ProjectID, &deploy.Status, &manifest, &deploy.FileCount,
		&deploy.TotalBytes, &deploy.SPAFallback, &deploy.Label, &deploy.FailureReason, &deploy.CreatedBy,
		&deploy.CreatedAt, &finishedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifest, &deploy.Manifest); err != nil {
		return nil, fmt.Errorf("decode deploy manifest: %w", err)
	}
	if finishedAt.Valid {
		finished := finishedAt.Time
		deploy.FinishedAt = &finished
	}
	return deploy, nil
}

func (s *PostgresStore) GetDeploy(ctx context.Context, projectID, siteID, id string) (*Deploy, error) {
	deploy, err := scanDeploy(s.db.QueryRowContext(ctx, `SELECT `+deployColumns+` FROM site_deploys
		WHERE project_id = $1 AND site_id = $2 AND id = $3`, projectID, siteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read site deploy: %w", err)
	}
	return deploy, nil
}

func (s *PostgresStore) ListDeploys(ctx context.Context, projectID, siteID string, limit int) ([]*Deploy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deployColumns+` FROM site_deploys
		WHERE project_id = $1 AND site_id = $2 ORDER BY created_at DESC, id LIMIT $3`, projectID, siteID, limit)
	if err != nil {
		return nil, fmt.Errorf("list site deploys: %w", err)
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		deploy, err := scanDeploy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan site deploy: %w", err)
		}
		out = append(out, deploy)
	}
	return out, rows.Err()
}
