package sitebuilds

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const appConnectionColumns = `app_id, project_id, provider, installation_id, repository_id, repository, branch, builder,
	root_dir, dockerfile_path, build_args, connected_by, COALESCE(inactive_reason, ''), created_at, updated_at`

// cancelAppBuildsSQL stops apps' queued and running builds, named by matching project and app id arrays.
const cancelAppBuildsSQL = `UPDATE site_builds SET status = 'CANCELLED', failure_reason = $3, finished_at = NOW(),
	lease_holder = NULL, lease_until = NULL WHERE kind = 'app' AND status IN ('QUEUED', 'RUNNING')
	AND (project_id, app_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`

func scanAppConnection(row scanner) (*AppConnection, error) {
	var connection AppConnection
	var args []byte
	settings := &connection.Settings
	err := row.Scan(&connection.AppID, &connection.ProjectID, &connection.Provider, &connection.InstallationID, &connection.RepositoryID,
		&connection.Repository, &settings.Branch, &settings.Builder, &settings.RootDir, &settings.DockerfilePath, &args,
		&connection.ConnectedBy, &connection.InactiveReason, &connection.CreatedAt, &connection.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &settings.BuildArgs); err != nil {
		return nil, fmt.Errorf("decode build arguments: %w", err)
	}
	return &connection, nil
}

func (s *PostgresStore) GetAppConnection(ctx context.Context, projectID, appID string) (*AppConnection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appConnectionColumns+` FROM app_git_connections WHERE project_id = $1 AND app_id = $2`,
		projectID, appID)
	connection, err := scanAppConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return connection, err
}

func (s *PostgresStore) SaveAppConnection(ctx context.Context, connection *AppConnection) error {
	args, err := json.Marshal(connection.Settings.BuildArgs)
	if err != nil {
		return fmt.Errorf("encode build arguments: %w", err)
	}
	settings := connection.Settings
	result, err := s.db.ExecContext(ctx, `INSERT INTO app_git_connections
		(project_id, app_id, provider, installation_id, repository_id, repository, branch, builder, root_dir, dockerfile_path,
		 build_args, connected_by, created_at, updated_at)
		SELECT project_id, id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), NOW() FROM apps WHERE project_id = $1 AND id = $2
		ON CONFLICT (project_id, app_id) DO UPDATE SET provider = EXCLUDED.provider, installation_id = EXCLUDED.installation_id,
		  repository_id = EXCLUDED.repository_id, repository = EXCLUDED.repository, branch = EXCLUDED.branch,
		  builder = EXCLUDED.builder, root_dir = EXCLUDED.root_dir, dockerfile_path = EXCLUDED.dockerfile_path,
		  build_args = EXCLUDED.build_args, connected_by = EXCLUDED.connected_by, inactive_reason = NULL, updated_at = NOW()`,
		connection.ProjectID, connection.AppID, connection.Provider, connection.InstallationID, connection.RepositoryID,
		connection.Repository, settings.Branch, settings.Builder, settings.RootDir, settings.DockerfilePath, args, connection.ConnectedBy)
	if err != nil {
		return fmt.Errorf("save the app's repository: %w", err)
	}
	return requireTouched(result, apphost.ErrAppNotFound)
}

func (s *PostgresStore) DeleteAppConnection(ctx context.Context, projectID, appID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM app_git_connections WHERE project_id = $1 AND app_id = $2`, projectID, appID)
	if err != nil {
		return fmt.Errorf("disconnect the app's repository: %w", err)
	}
	return requireTouched(result, ErrNotConnected)
}

func (s *PostgresStore) AppConnectionsForPush(ctx context.Context, provider string, installationID, repositoryID int64, branch string) ([]*AppConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appConnectionColumns+` FROM app_git_connections
		WHERE provider = $1 AND installation_id = $2 AND repository_id = $3 AND branch = $4`, provider, installationID, repositoryID, branch)
	if err != nil {
		return nil, fmt.Errorf("find the apps a push builds: %w", err)
	}
	defer rows.Close()
	var found []*AppConnection
	for rows.Next() {
		connection, err := scanAppConnection(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, connection)
	}
	return found, rows.Err()
}

// forgetAppConnections drops the apps' connections through an installation (or to the given repositories) and cancels their builds.
func forgetAppConnections(ctx context.Context, tx *sql.Tx, provider string, installationID int64, repositoryIDs []int64) (int, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM app_git_connections WHERE provider = $1 AND installation_id = $2
		AND (cardinality($3::bigint[]) = 0 OR repository_id = ANY($3::bigint[])) RETURNING project_id, app_id`,
		provider, installationID, pq.Array(repositoryIDs))
	if err != nil {
		return 0, fmt.Errorf("forget an installation's app connections: %w", err)
	}
	var projects, apps []string
	for rows.Next() {
		var projectID, appID string
		if err := rows.Scan(&projectID, &appID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		projects, apps = append(projects, projectID), append(apps, appID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("forget an installation's app connections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, cancelAppBuildsSQL, pq.Array(projects), pq.Array(apps), appLostReason); err != nil {
		return 0, fmt.Errorf("cancel the forgotten apps' builds: %w", err)
	}
	return len(apps), nil
}
