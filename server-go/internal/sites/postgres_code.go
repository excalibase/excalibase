package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// PruneCode keeps the newest keep succeeded deploys' code and the live deploy's, and deletes the rest.
func (s *PostgresStore) PruneCode(ctx context.Context, projectID, siteID string, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM site_deploy_code c WHERE c.project_id = $1 AND c.site_id = $2
		AND c.deploy_id NOT IN (SELECT live_deploy_id FROM sites WHERE id = $2 AND live_deploy_id IS NOT NULL)
		AND c.deploy_id NOT IN (SELECT d.id FROM site_deploys d WHERE d.project_id = $1 AND d.site_id = $2 AND d.status = 'SUCCEEDED'
			ORDER BY d.created_at DESC, d.id LIMIT $3)`, projectID, siteID, keep)
	if err != nil {
		return fmt.Errorf("prune kept server code: %w", err)
	}
	return nil
}
