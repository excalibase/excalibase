package sites

import (
	"context"
	"database/sql"
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
