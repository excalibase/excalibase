package sitebuilds

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// PostgresStore keeps git builds in the platform database.
type PostgresStore struct{ db *sql.DB }

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) GetConnection(ctx context.Context, projectID, siteID string) (*Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT site_id, project_id, provider, installation_id, repository_id, repository, branch,
		root_dir, install_command, build_command, output_dir, node_version, build_env, connected_by, COALESCE(inactive_reason, ''), created_at, updated_at
		FROM site_git_connections WHERE site_id = $1 AND project_id = $2`, siteID, projectID)
	connection, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return connection, err
}

type scanner interface{ Scan(dest ...any) error }

func scanConnection(row scanner) (*Connection, error) {
	var connection Connection
	var env []byte
	settings := &connection.Settings
	err := row.Scan(&connection.SiteID, &connection.ProjectID, &connection.Provider, &connection.InstallationID, &connection.RepositoryID,
		&connection.Repository, &settings.Branch, &settings.RootDir, &settings.InstallCommand, &settings.BuildCommand, &settings.OutputDir,
		&settings.NodeVersion, &env, &connection.ConnectedBy, &connection.InactiveReason, &connection.CreatedAt, &connection.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(env, &settings.Env); err != nil {
		return nil, fmt.Errorf("decode build variables: %w", err)
	}
	return &connection, nil
}

func (s *PostgresStore) SaveConnection(ctx context.Context, connection *Connection) error {
	env, err := json.Marshal(connection.Settings.Env)
	if err != nil {
		return fmt.Errorf("encode build variables: %w", err)
	}
	settings := connection.Settings
	result, err := s.db.ExecContext(ctx, `INSERT INTO site_git_connections
		(site_id, project_id, provider, installation_id, repository_id, repository, branch, root_dir, install_command,
		 build_command, output_dir, node_version, build_env, connected_by, created_at, updated_at)
		SELECT id, project_id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW() FROM sites WHERE id = $1 AND project_id = $2
		ON CONFLICT (site_id) DO UPDATE SET provider = EXCLUDED.provider, installation_id = EXCLUDED.installation_id,
		  repository_id = EXCLUDED.repository_id, repository = EXCLUDED.repository, branch = EXCLUDED.branch,
		  root_dir = EXCLUDED.root_dir, install_command = EXCLUDED.install_command, build_command = EXCLUDED.build_command,
		  output_dir = EXCLUDED.output_dir, node_version = EXCLUDED.node_version, build_env = EXCLUDED.build_env,
		  connected_by = EXCLUDED.connected_by, inactive_reason = NULL, updated_at = NOW()`,
		connection.SiteID, connection.ProjectID, connection.Provider, connection.InstallationID, connection.RepositoryID,
		connection.Repository, settings.Branch, settings.RootDir, settings.InstallCommand, settings.BuildCommand,
		settings.OutputDir, settings.NodeVersion, env, connection.ConnectedBy)
	if err != nil {
		return fmt.Errorf("save the site's repository: %w", err)
	}
	return requireTouched(result, sites.ErrSiteNotFound)
}

func requireTouched(result sql.Result, missing error) error {
	touched, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if touched == 0 {
		return missing
	}
	return nil
}

func (s *PostgresStore) DeleteConnection(ctx context.Context, projectID, siteID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM site_git_connections WHERE site_id = $1 AND project_id = $2`, siteID, projectID)
	if err != nil {
		return fmt.Errorf("disconnect the site's repository: %w", err)
	}
	return requireTouched(result, ErrNotConnected)
}

func (s *PostgresStore) ConnectionsForPush(ctx context.Context, provider string, installationID, repositoryID int64, branch string) ([]*Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT site_id, project_id, provider, installation_id, repository_id, repository, branch,
		root_dir, install_command, build_command, output_dir, node_version, build_env, connected_by, COALESCE(inactive_reason, ''), created_at, updated_at
		FROM site_git_connections WHERE provider = $1 AND installation_id = $2 AND repository_id = $3 AND branch = $4`,
		provider, installationID, repositoryID, branch)
	if err != nil {
		return nil, fmt.Errorf("find the sites a push builds: %w", err)
	}
	defer rows.Close()
	var found []*Connection
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, connection)
	}
	return found, rows.Err()
}

func (s *PostgresStore) ForgetInstallation(ctx context.Context, provider string, installationID int64, repositoryIDs []int64) (int, error) {
	if repositoryIDs == nil {
		repositoryIDs = []int64{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin forgetting an installation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, forgetInstallationSQL, provider, installationID, pq.Array(repositoryIDs))
	if err != nil {
		return 0, fmt.Errorf("forget an installation's connections: %w", err)
	}
	var forgotten []string
	for rows.Next() {
		var siteID string
		if err := rows.Scan(&siteID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		forgotten = append(forgotten, siteID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("forget an installation's connections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, cancelBuildsSQL, pq.Array(forgotten), appLostReason); err != nil {
		return 0, fmt.Errorf("cancel the forgotten sites' builds: %w", err)
	}
	apps, err := forgetAppConnections(ctx, tx, provider, installationID, repositoryIDs)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, forgetGrantsSQL, provider, installationID, pq.Array(repositoryIDs)); err != nil {
		return 0, fmt.Errorf("forget an installation's grants: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("forget an installation: %w", err)
	}
	return len(forgotten) + apps, nil
}

const appLostReason = "the GitHub App no longer covers the repository"

func (s *PostgresStore) SaveSignIn(ctx context.Context, stateHash string, signIn SignIn, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM site_git_states WHERE expires_at < NOW()`); err != nil {
		return fmt.Errorf("clear expired sign-ins: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_git_states (state_hash, user_id, project_id, site_id, browser_hash, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		stateHash, signIn.UserID, signIn.ProjectID, signIn.SiteID, signIn.BrowserHash, expires)
	if err != nil {
		return fmt.Errorf("record the sign-in: %w", err)
	}
	return nil
}

func (s *PostgresStore) TakeSignIn(ctx context.Context, stateHash string, now time.Time) (SignIn, error) {
	var signIn SignIn
	err := s.db.QueryRowContext(ctx, `DELETE FROM site_git_states WHERE state_hash = $1 AND expires_at > $2
		RETURNING user_id, project_id, site_id, browser_hash`, stateHash, now).Scan(&signIn.UserID, &signIn.ProjectID, &signIn.SiteID, &signIn.BrowserHash)
	if errors.Is(err, sql.ErrNoRows) {
		return SignIn{}, ErrStateInvalid
	}
	if err != nil {
		return SignIn{}, fmt.Errorf("read the sign-in: %w", err)
	}
	return signIn, nil
}

func (s *PostgresStore) ReplaceGrants(ctx context.Context, userID, provider string, access []gitprovider.Access, verifiedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin grant update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_git_grants WHERE user_id = $1 AND provider = $2`, userID, provider); err != nil {
		return fmt.Errorf("clear the user's grants: %w", err)
	}
	for _, installation := range access {
		for _, repository := range installation.Repositories {
			_, err := tx.ExecContext(ctx, `INSERT INTO site_git_grants (user_id, provider, installation_id, account, repository_id,
				repository, default_branch, private, verified_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT DO NOTHING`,
				userID, provider, installation.Installation.ID, installation.Installation.Account, repository.ID,
				repository.FullName, repository.DefaultBranch, repository.Private, verifiedAt)
			if err != nil {
				return fmt.Errorf("record a grant: %w", err)
			}
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) Grants(ctx context.Context, userID, provider string, since time.Time) ([]gitprovider.Access, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT installation_id, account, repository_id, repository, default_branch, private
		FROM site_git_grants WHERE user_id = $1 AND provider = $2 AND verified_at >= $3 ORDER BY account, repository`, userID, provider, since)
	if err != nil {
		return nil, fmt.Errorf("read the user's grants: %w", err)
	}
	defer rows.Close()
	var access []gitprovider.Access
	index := map[int64]int{}
	for rows.Next() {
		var installation gitprovider.Installation
		var repository gitprovider.Repository
		if err := rows.Scan(&installation.ID, &installation.Account, &repository.ID, &repository.FullName,
			&repository.DefaultBranch, &repository.Private); err != nil {
			return nil, err
		}
		position, seen := index[installation.ID]
		if !seen {
			position = len(access)
			index[installation.ID] = position
			access = append(access, gitprovider.Access{Installation: installation})
		}
		access[position].Repositories = append(access[position].Repositories, repository)
	}
	return access, rows.Err()
}

func (s *PostgresStore) VisibleSecrets(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM site_build_visible_secrets WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the build-visible secrets: %w", err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *PostgresStore) SetVisibleSecrets(ctx context.Context, projectID string, names []string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin visibility update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// A nil slice is SQL NULL, and NOT (name = ANY(NULL)) matches nothing: an empty set must hide every secret.
	kept := append([]string{}, names...)
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_build_visible_secrets WHERE project_id = $1 AND NOT (name = ANY($2))`,
		projectID, pq.Array(kept)); err != nil {
		return fmt.Errorf("hide secrets from builds: %w", err)
	}
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_build_visible_secrets (project_id, name, marked_by) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, projectID, name, actor); err != nil {
			return fmt.Errorf("show a secret to builds: %w", err)
		}
	}
	return tx.Commit()
}

// forgetInstallationSQL drops an installation's connections; an empty repository list means all of them.
const forgetInstallationSQL = `DELETE FROM site_git_connections WHERE provider = $1 AND installation_id = $2
	AND (cardinality($3::bigint[]) = 0 OR repository_id = ANY($3::bigint[])) RETURNING site_id`

// forgetGrantsSQL drops the sign-in grants for the same repositories.
const forgetGrantsSQL = `DELETE FROM site_git_grants WHERE provider = $1 AND installation_id = $2
	AND (cardinality($3::bigint[]) = 0 OR repository_id = ANY($3::bigint[]))`

// cancelBuildsSQL stops sites' queued and running builds; a running one's lease is gone, so its dispatcher stops it.
const cancelBuildsSQL = `UPDATE site_builds SET status = 'CANCELLED', failure_reason = $2, finished_at = NOW(),
	lease_holder = NULL, lease_until = NULL WHERE site_id = ANY($1) AND status IN ('QUEUED', 'RUNNING')`

func (s *PostgresStore) CancelBuilds(ctx context.Context, target Target, reason string) error {
	var err error
	if target.Kind == KindApp {
		_, err = s.db.ExecContext(ctx, cancelAppBuildsSQL, pq.Array([]string{target.ProjectID}), pq.Array([]string{target.ID}), reason)
	} else {
		_, err = s.db.ExecContext(ctx, cancelBuildsSQL, pq.Array([]string{target.ID}), reason)
	}
	if err != nil {
		return fmt.Errorf("cancel the %s's builds: %w", target.Kind, err)
	}
	return nil
}

func (s *PostgresStore) DeactivateConnection(ctx context.Context, target Target, reason string) error {
	var err error
	if target.Kind == KindApp {
		_, err = s.db.ExecContext(ctx, `UPDATE app_git_connections SET inactive_reason = $3, updated_at = NOW()
			WHERE project_id = $1 AND app_id = $2`, target.ProjectID, target.ID, reason)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE site_git_connections SET inactive_reason = $2, updated_at = NOW() WHERE site_id = $1`,
			target.ID, reason)
	}
	if err != nil {
		return fmt.Errorf("deactivate the %s's repository: %w", target.Kind, err)
	}
	return nil
}

func (s *PostgresStore) RenameRepository(ctx context.Context, target Target, repositoryID int64, fullName string) error {
	var err error
	if target.Kind == KindApp {
		_, err = s.db.ExecContext(ctx, `UPDATE app_git_connections SET repository = $4, updated_at = NOW()
			WHERE project_id = $1 AND app_id = $2 AND repository_id = $3`, target.ProjectID, target.ID, repositoryID, fullName)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE site_git_connections SET repository = $3, updated_at = NOW()
			WHERE site_id = $1 AND repository_id = $2`, target.ID, repositoryID, fullName)
	}
	if err != nil {
		return fmt.Errorf("record the repository's new name: %w", err)
	}
	return nil
}
