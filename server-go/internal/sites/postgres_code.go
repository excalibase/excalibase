package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var _ CodeStore = (*PostgresStore)(nil)

// PutCode keeps the deploy's code in the platform database, beside its record.
func (s *PostgresStore) PutCode(ctx context.Context, deploy *Deploy, packed []byte) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO site_deploy_code (deploy_id, site_id, project_id, packed)
		SELECT id, site_id, project_id, $4 FROM site_deploys WHERE id = $1 AND site_id = $2 AND project_id = $3`,
		deploy.ID, deploy.SiteID, deploy.ProjectID, packed)
	if err != nil {
		return fmt.Errorf("keep server code: %w", err)
	}
	if kept, err := result.RowsAffected(); err != nil || kept == 0 {
		return errors.Join(ErrDeployNotFound, err)
	}
	return nil
}

func (s *PostgresStore) GetCode(ctx context.Context, projectID, siteID, deployID string) ([]byte, error) {
	var packed []byte
	err := s.db.QueryRowContext(ctx, `SELECT packed FROM site_deploy_code WHERE project_id = $1 AND site_id = $2 AND deploy_id = $3`,
		projectID, siteID, deployID).Scan(&packed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrServerCodeGone
	}
	if err != nil {
		return nil, fmt.Errorf("read kept server code: %w", err)
	}
	return packed, nil
}

// MarkPublished stamps the deploy as the one Cloudflare runs.
func (s *PostgresStore) MarkPublished(ctx context.Context, deploy *Deploy, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE site_deploys SET published_at = $4 WHERE id = $1 AND site_id = $2 AND project_id = $3`,
		deploy.ID, deploy.SiteID, deploy.ProjectID, at)
	if err != nil {
		return fmt.Errorf("record the published deploy: %w", err)
	}
	return nil
}

// PruneCode keeps the newest succeeded deploys' code that fits the retention,
// the live deploy's, the last published one's and recent pending ones', and deletes the rest.
func (s *PostgresStore) PruneCode(ctx context.Context, projectID, siteID string, retention CodeRetention) error {
	_, err := s.db.ExecContext(ctx, `WITH ranked AS (
			SELECT c.deploy_id,
				row_number() OVER (ORDER BY d.created_at DESC, d.id) AS position,
				sum(octet_length(c.packed)) OVER (ORDER BY d.created_at DESC, d.id) AS running
			FROM site_deploy_code c JOIN site_deploys d ON d.id = c.deploy_id
			WHERE c.project_id = $1 AND c.site_id = $2 AND d.status = 'SUCCEEDED'),
		kept AS (
			SELECT deploy_id AS id FROM ranked WHERE position <= $3 AND running <= $4
			UNION SELECT live_deploy_id FROM sites WHERE id = $2 AND project_id = $1 AND live_deploy_id IS NOT NULL
			UNION SELECT id FROM site_deploys WHERE project_id = $1 AND site_id = $2 AND status = 'PENDING' AND created_at > $5
			UNION (SELECT id FROM site_deploys WHERE project_id = $1 AND site_id = $2 AND published_at IS NOT NULL
				ORDER BY published_at DESC LIMIT 1))
		DELETE FROM site_deploy_code c WHERE c.project_id = $1 AND c.site_id = $2 AND c.deploy_id NOT IN (SELECT id FROM kept)`,
		projectID, siteID, retention.Keep, retention.MaxBytes, retention.PendingSince)
	if err != nil {
		return fmt.Errorf("prune kept server code: %w", err)
	}
	return nil
}
