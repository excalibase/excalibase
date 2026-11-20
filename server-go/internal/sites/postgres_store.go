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

// siteLimitLockSpace keeps the per-project site count apart from other advisory locks.
const siteLimitLockSpace = 353

// PostgresStore keeps sites in the platform database.
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

var _ Store = (*PostgresStore)(nil)

type rowScanner interface{ Scan(dest ...any) error }

func scanSite(row rowScanner) (*Site, error) {
	site := &Site{}
	err := row.Scan(&site.ID, &site.ProjectID, &site.Name, &site.Hostname, &site.Kind, &site.SPAFallback,
		&site.LiveDeployID, &site.CorsClaimed, &site.CreatedBy, &site.CreatedAt, &site.UpdatedAt)
	return site, err
}

// siteKind is the kind a site is stored as; one built without a kind is static.
func siteKind(site *Site) string {
	if site.Kind == "" {
		return KindStatic
	}
	return site.Kind
}

// Create counts the project's sites under a per-project lock, so two creates cannot both take the last slot.
func (s *PostgresStore) Create(ctx context.Context, site *Site, maxSites, maxServerSites int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin site create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, siteLimitLockSpace, site.ProjectID); err != nil {
		return fmt.Errorf("lock project site count: %w", err)
	}
	var held, heldServer int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE kind = 'server') FROM sites WHERE project_id = $1`,
		site.ProjectID).Scan(&held, &heldServer); err != nil {
		return fmt.Errorf("count project sites: %w", err)
	}
	if held >= maxSites {
		return ErrSiteLimitReached
	}
	if siteKind(site) == KindServer && heldServer >= maxServerSites {
		return ErrServerSiteLimitReached
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sites (id, project_id, name, hostname, kind, spa_fallback, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		site.ID, site.ProjectID, site.Name, site.Hostname, siteKind(site), site.SPAFallback, site.CreatedBy, site.CreatedAt, site.UpdatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return ErrSiteNameTaken
	}
	if err != nil {
		return fmt.Errorf("create site: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) Get(ctx context.Context, projectID, id string) (*Site, error) {
	site, err := scanSite(s.db.QueryRowContext(ctx, `SELECT id, project_id, name, hostname, kind, spa_fallback, COALESCE(live_deploy_id, ''), cors_claimed, created_by, created_at, updated_at
		FROM sites WHERE project_id = $1 AND id = $2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read site: %w", err)
	}
	return site, nil
}

func (s *PostgresStore) List(ctx context.Context, projectID string) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, name, hostname, kind, spa_fallback, COALESCE(live_deploy_id, ''), cors_claimed, created_by, created_at, updated_at
		FROM sites WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	out := []*Site{}
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListServerSites(ctx context.Context) ([]*Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, name, hostname, kind, spa_fallback, COALESCE(live_deploy_id, ''), cors_claimed, created_by, created_at, updated_at
		FROM sites WHERE kind = 'server' ORDER BY project_id, name`)
	if err != nil {
		return nil, fmt.Errorf("list server sites: %w", err)
	}
	defer rows.Close()
	out := []*Site{}
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateSettings(ctx context.Context, projectID, id string, spaFallback bool) (*Site, error) {
	site, err := scanSite(s.db.QueryRowContext(ctx, `UPDATE sites SET spa_fallback = $3, updated_at = NOW()
		WHERE project_id = $1 AND id = $2 RETURNING id, project_id, name, hostname, kind, spa_fallback, COALESCE(live_deploy_id, ''), cors_claimed, created_by, created_at, updated_at`, projectID, id, spaFallback))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSiteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update site settings: %w", err)
	}
	return site, nil
}

func (s *PostgresStore) Delete(ctx context.Context, projectID, id, lease string) error {
	return s.execUnderLease(ctx, "delete site", projectID, id, `DELETE FROM sites
		WHERE project_id = $1 AND id = $2 AND deploy_lease = $3 AND deploying_until > NOW()`, projectID, id, lease)
}

func (s *PostgresStore) ClaimDeploy(ctx context.Context, projectID, id, lease string, until time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sites SET deploying_until = $3, deploy_lease = $4
		WHERE project_id = $1 AND id = $2 AND (deploying_until IS NULL OR deploying_until < NOW())`, projectID, id, until, lease)
	if err != nil {
		return fmt.Errorf("claim site deploy: %w", err)
	}
	if claimed, err := result.RowsAffected(); err != nil || claimed == 1 {
		return err
	}
	site, err := s.Get(ctx, projectID, id)
	if err != nil {
		return err
	}
	if site == nil {
		return ErrSiteNotFound
	}
	return ErrSiteBusy
}

func (s *PostgresStore) ReleaseDeploy(ctx context.Context, projectID, id, lease string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE sites SET deploying_until = NULL, deploy_lease = NULL
		WHERE project_id = $1 AND id = $2 AND deploy_lease = $3`, projectID, id, lease); err != nil {
		return fmt.Errorf("release site deploy: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkCorsClaimed(ctx context.Context, projectID, id string) error {
	return s.execOne(ctx, "mark site origin", `UPDATE sites SET cors_claimed = TRUE WHERE project_id = $1 AND id = $2`, projectID, id)
}

func (s *PostgresStore) SetLive(ctx context.Context, projectID, siteID, deployID, lease string) error {
	return s.execUnderLease(ctx, "set live deploy", projectID, siteID, `UPDATE sites SET live_deploy_id = $4, updated_at = NOW()
		WHERE project_id = $1 AND id = $2 AND deploy_lease = $3 AND deploying_until > NOW()`, projectID, siteID, lease, deployID)
}

// execUnderLease runs a write guarded by the lease, telling a lost lease from a missing site.
func (s *PostgresStore) execUnderLease(ctx context.Context, what, projectID, siteID, query string, args ...any) error {
	err := s.execOne(ctx, what, query, args...)
	if !errors.Is(err, ErrSiteNotFound) {
		return err
	}
	site, getErr := s.Get(ctx, projectID, siteID)
	if getErr != nil {
		return getErr
	}
	if site != nil {
		return ErrLeaseLost
	}
	return ErrSiteNotFound
}

// execOne runs a write that must touch exactly the named site.
func (s *PostgresStore) execOne(ctx context.Context, what, query string, args ...any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	touched, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if touched == 0 {
		return ErrSiteNotFound
	}
	return nil
}

func (s *PostgresStore) RuntimeVars(ctx context.Context, projectID, id string) (map[string]string, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(v.vars, '{}'::jsonb) FROM sites s LEFT JOIN site_runtime_vars v ON v.site_id = s.id
		WHERE s.project_id = $1 AND s.id = $2`, projectID, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSiteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read runtime variables: %w", err)
	}
	vars := map[string]string{}
	if err := json.Unmarshal(raw, &vars); err != nil {
		return nil, fmt.Errorf("decode runtime variables: %w", err)
	}
	return vars, nil
}

func (s *PostgresStore) SetRuntimeVars(ctx context.Context, projectID, id string, vars map[string]string) error {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("encode runtime variables: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO site_runtime_vars (site_id, vars, updated_at)
		SELECT id, $3::jsonb, NOW() FROM sites WHERE project_id = $1 AND id = $2
		ON CONFLICT (site_id) DO UPDATE SET vars = EXCLUDED.vars, updated_at = NOW()`, projectID, id, encoded)
	if err != nil {
		return fmt.Errorf("save runtime variables: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrSiteNotFound
	}
	return nil
}

func (s *PostgresStore) ServerVisibleSecrets(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM site_server_visible_secrets WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the server-visible secrets: %w", err)
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

func (s *PostgresStore) SetServerVisibleSecrets(ctx context.Context, projectID string, names []string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin server-visibility update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// A nil slice is SQL NULL and would match nothing: an empty set must hide every secret.
	kept := append([]string{}, names...)
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_server_visible_secrets WHERE project_id = $1 AND NOT (name = ANY($2))`,
		projectID, pq.Array(kept)); err != nil {
		return fmt.Errorf("hide secrets from server sites: %w", err)
	}
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_server_visible_secrets (project_id, name, marked_by) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, projectID, name, actor); err != nil {
			return fmt.Errorf("show a secret to server sites: %w", err)
		}
	}
	return tx.Commit()
}
