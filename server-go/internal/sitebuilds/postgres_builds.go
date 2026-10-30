package sitebuilds

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// dispatchLock serialises claims across replicas, so the slot count read and the claim are one step.
const dispatchLock = 354_000_001

const supersededReason = "superseded by a newer commit before it started"

// The queries below spell out the columns scanBuild reads, in its order, and one
// target's match: $1 the project, $2 the kind, $3 the site or app id. The OR lets
// each kind use its own index; the one-target CHECK keeps the other column NULL.
const (
	claimBuildSQL = `UPDATE site_builds SET status = 'RUNNING', started_at = NOW(), lease_holder = $2, lease_until = $3
		WHERE id = $1 RETURNING id, kind, COALESCE(site_id, ''), COALESCE(app_id, ''), project_id, status, trigger, commit_sha,
	COALESCE(commit_message, ''), settings, COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds,
	project_concurrency, COALESCE(deploy_id, ''), COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by,
	created_at, started_at, finished_at`
	listBuildsSQL = `SELECT id, kind, COALESCE(site_id, ''), COALESCE(app_id, ''), project_id, status, trigger, commit_sha,
	COALESCE(commit_message, ''), settings, COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds,
	project_concurrency, COALESCE(deploy_id, ''), COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by,
	created_at, started_at, finished_at
		FROM site_builds WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3) ORDER BY created_at DESC LIMIT $4`
	getBuildSQL = `SELECT id, kind, COALESCE(site_id, ''), COALESCE(app_id, ''), project_id, status, trigger, commit_sha,
	COALESCE(commit_message, ''), settings, COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds,
	project_concurrency, COALESCE(deploy_id, ''), COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by,
	created_at, started_at, finished_at, COALESCE(log, '')
		FROM site_builds WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3) AND id = $4`
	deliverySQL = `SELECT EXISTS (SELECT 1 FROM site_builds
		WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3) AND delivery_id = $4)`
	supersedeSQL = `UPDATE site_builds SET status = 'CANCELLED', failure_reason = $4, finished_at = NOW()
		WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3) AND status = 'QUEUED'`
	trimSQL = `DELETE FROM site_builds WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3)
		AND status IN ('SUCCEEDED', 'FAILED', 'CANCELLED')
		AND id NOT IN (SELECT id FROM site_builds WHERE project_id = $1 AND kind = $2 AND (site_id = $3 OR app_id = $3)
			ORDER BY created_at DESC LIMIT $4)`
	// insertBuildSQL writes the build only when the project holds the site or app it names.
	insertBuildSQL = `INSERT INTO site_builds (id, kind, site_id, app_id, project_id, status, trigger, commit_sha, commit_message,
		settings, delivery_id, cpu_millis, memory, disk, timeout_seconds, project_concurrency, requested_by, created_at,
		failure_kind, failure_reason, finished_at)
		SELECT $4, $2, CASE WHEN $2 = 'site' THEN $3 END, CASE WHEN $2 = 'app' THEN $3 END, $1, $5, $6, $7, NULLIF($8, ''), $9,
		NULLIF($10, ''), $11, $12, $13, $14, $15, $16, $17, NULLIF($18, ''), NULLIF($19, ''), CASE WHEN $5 = 'QUEUED' THEN NULL ELSE NOW() END
		WHERE EXISTS (SELECT 1 FROM sites WHERE $2 = 'site' AND id = $3 AND project_id = $1)
		   OR EXISTS (SELECT 1 FROM apps WHERE $2 = 'app' AND id = $3 AND project_id = $1)`
	// claimableSQL finds the oldest queued build of the kinds asked for that fits: running builds of every
	// kind fill the install's slots, one runs per site or app, and the project's cap counts its own kind.
	claimableSQL = `SELECT queued.id FROM site_builds queued
		WHERE queued.status = 'QUEUED' AND queued.kind = ANY($2)
		  AND (SELECT COUNT(*) FROM site_builds WHERE status = 'RUNNING') < $1
		  AND NOT EXISTS (SELECT 1 FROM site_builds running WHERE running.status = 'RUNNING' AND running.kind = queued.kind
		      AND running.project_id = queued.project_id AND (running.site_id = queued.site_id OR running.app_id = queued.app_id))
		  AND (SELECT COUNT(*) FROM site_builds WHERE status = 'RUNNING' AND project_id = queued.project_id
		      AND kind = queued.kind) < queued.project_concurrency
		ORDER BY queued.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`
)

func targetArgs(target Target, more ...any) []any {
	return append([]any{target.ProjectID, target.Kind, target.ID}, more...)
}

// Enqueue writes build as given: QUEUED supersedes the target's queued ones; a
// build refused before it could queue is written FAILED, so its push is not lost silently.
func (s *PostgresStore) Enqueue(ctx context.Context, build *Build, keep int) (bool, error) {
	snapshot, err := json.Marshal(buildSnapshot{Settings: build.Settings, Provider: build.Provider,
		InstallationID: build.InstallationID, RepositoryID: build.RepositoryID, Repository: build.Repository, App: build.App})
	if err != nil {
		return false, fmt.Errorf("encode the build's settings: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	target := build.Target()
	if build.DeliveryID != "" {
		var seen bool
		if err := tx.QueryRowContext(ctx, deliverySQL, targetArgs(target, build.DeliveryID)...).Scan(&seen); err != nil {
			return false, fmt.Errorf("check the delivery: %w", err)
		}
		if seen {
			return false, nil
		}
	}
	if err := takeAppBuildSlot(ctx, tx, build); err != nil {
		return false, err
	}
	if build.Status == StatusQueued {
		if _, err := tx.ExecContext(ctx, supersedeSQL, targetArgs(target, supersededReason)...); err != nil {
			return false, fmt.Errorf("supersede queued builds: %w", err)
		}
	}
	limits := build.Limits
	result, err := tx.ExecContext(ctx, insertBuildSQL, targetArgs(target, build.ID, build.Status, build.Trigger, build.CommitSHA,
		build.CommitMessage, snapshot, build.DeliveryID, limits.CPUMillis, limits.Memory, limits.Disk, limits.TimeoutSeconds,
		limits.ProjectConcurrency, build.RequestedBy, build.CreatedAt, string(build.FailureKind), build.FailureReason)...)
	if err != nil {
		return false, fmt.Errorf("queue the build: %w", err)
	}
	if err := requireTouched(result, ErrNotConnected); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, trimSQL, targetArgs(target, keep)...); err != nil {
		return false, fmt.Errorf("trim the build history: %w", err)
	}
	return true, tx.Commit()
}

func (s *PostgresStore) ClaimNext(ctx context.Context, slots int, holder string, leaseUntil time.Time, kinds []string) (*Build, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, dispatchLock); err != nil {
		return nil, fmt.Errorf("take the dispatch lock: %w", err)
	}
	var id string
	err = tx.QueryRowContext(ctx, claimableSQL, slots, pq.Array(kinds)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find a build to run: %w", err)
	}
	row := tx.QueryRowContext(ctx, claimBuildSQL, id, holder, leaseUntil)
	build, err := scanBuild(row)
	if err != nil {
		return nil, fmt.Errorf("claim the build: %w", err)
	}
	return build, tx.Commit()
}

func scanBuild(row scanner) (*Build, error) {
	var build Build
	var snapshot []byte
	var started, finished sql.NullTime
	limits := &build.Limits
	err := row.Scan(&build.ID, &build.Kind, &build.SiteID, &build.AppID, &build.ProjectID, &build.Status, &build.Trigger, &build.CommitSHA, &build.CommitMessage,
		&snapshot, &build.DeliveryID, &limits.CPUMillis, &limits.Memory, &limits.Disk, &limits.TimeoutSeconds, &limits.ProjectConcurrency,
		&build.DeployID, &build.FailureKind, &build.FailureReason, &build.RequestedBy, &build.CreatedAt, &started, &finished)
	if err != nil {
		return nil, err
	}
	var decoded buildSnapshot
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		return nil, fmt.Errorf("decode the build's settings: %w", err)
	}
	build.Settings, build.Provider, build.InstallationID, build.RepositoryID, build.Repository, build.App =
		decoded.Settings, decoded.Provider, decoded.InstallationID, decoded.RepositoryID, decoded.Repository, decoded.App
	if started.Valid {
		build.StartedAt = &started.Time
	}
	if finished.Valid {
		build.FinishedAt = &finished.Time
	}
	return &build, nil
}

func (s *PostgresStore) ExtendLease(ctx context.Context, buildID, holder string, until time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_builds SET lease_until = $3 WHERE id = $1 AND lease_holder = $2 AND status = 'RUNNING'`,
		buildID, holder, until)
	if err != nil {
		return fmt.Errorf("extend the build's lease: %w", err)
	}
	return requireTouched(result, ErrLeaseLost)
}

func (s *PostgresStore) SaveLog(ctx context.Context, buildID, holder, log string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_builds SET log = $3 WHERE id = $1 AND lease_holder = $2 AND status = 'RUNNING'`,
		buildID, holder, log)
	if err != nil {
		return fmt.Errorf("save the build's log: %w", err)
	}
	return requireTouched(result, ErrLeaseLost)
}

func (s *PostgresStore) FinishBuild(ctx context.Context, build *Build, holder string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_builds SET status = $3, deploy_id = NULLIF($4, ''), failure_kind = NULLIF($5, ''),
		failure_reason = NULLIF($6, ''), log = $7, finished_at = NOW(), lease_holder = NULL, lease_until = NULL
		WHERE id = $1 AND lease_holder = $2 AND status = 'RUNNING'`,
		build.ID, holder, build.Status, build.DeployID, string(build.FailureKind), build.FailureReason, build.Log)
	if err != nil {
		return fmt.Errorf("record the build's outcome: %w", err)
	}
	return requireTouched(result, ErrLeaseLost)
}

func (s *PostgresStore) ExpireLostBuilds(ctx context.Context, now time.Time, reason string) ([]LostBuild, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE site_builds SET status = 'FAILED', failure_kind = 'platform', failure_reason = $2,
		finished_at = NOW(), lease_holder = NULL, lease_until = NULL
		WHERE status = 'RUNNING' AND lease_until < $1 RETURNING id, kind`, now, reason)
	if err != nil {
		return nil, fmt.Errorf("expire lost builds: %w", err)
	}
	defer rows.Close()
	var lost []LostBuild
	for rows.Next() {
		var build LostBuild
		if err := rows.Scan(&build.ID, &build.Kind); err != nil {
			return nil, err
		}
		lost = append(lost, build)
	}
	return lost, rows.Err()
}

func (s *PostgresStore) ListBuilds(ctx context.Context, target Target, limit int) ([]*Build, error) {
	rows, err := s.db.QueryContext(ctx, listBuildsSQL, targetArgs(target, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list the %s's builds: %w", target.Kind, err)
	}
	defer rows.Close()
	builds := []*Build{}
	for rows.Next() {
		build, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, rows.Err()
}

func (s *PostgresStore) GetBuild(ctx context.Context, target Target, id string) (*Build, error) {
	row := s.db.QueryRowContext(ctx, getBuildSQL, targetArgs(target, id)...)
	var logText string
	build, err := scanBuildWith(row, &logText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the build: %w", err)
	}
	build.Log = logText
	return build, nil
}

// scanBuildWith scans a build row followed by extra columns.
func scanBuildWith(row scanner, extra ...any) (*Build, error) {
	return scanBuild(scannerFunc(func(dest ...any) error { return row.Scan(append(dest, extra...)...) }))
}

type scannerFunc func(dest ...any) error

func (f scannerFunc) Scan(dest ...any) error { return f(dest...) }

// deliveryMemory outlasts GitHub's redelivery window, so a captured delivery cannot be replayed under its own id.
const deliveryMemory = 7 * 24 * time.Hour

func (s *PostgresStore) DeliverySeen(ctx context.Context, provider, deliveryID string) (bool, error) {
	var seen bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM site_git_deliveries WHERE provider = $1 AND delivery_id = $2)`,
		provider, deliveryID).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("read the delivery: %w", err)
	}
	return seen, nil
}

func (s *PostgresStore) RecordDelivery(ctx context.Context, provider, deliveryID string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM site_git_deliveries WHERE received_at < $1`, now.Add(-deliveryMemory)); err != nil {
		return fmt.Errorf("forget old deliveries: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_git_deliveries (provider, delivery_id, received_at) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, provider, deliveryID, now)
	if err != nil {
		return fmt.Errorf("record the delivery: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeliveryQueued(ctx context.Context, target Target, deliveryID string) (bool, error) {
	var queued bool
	if err := s.db.QueryRowContext(ctx, deliverySQL, targetArgs(target, deliveryID)...).Scan(&queued); err != nil {
		return false, fmt.Errorf("read the %s's delivery: %w", target.Kind, err)
	}
	return queued, nil
}

func (s *PostgresStore) RequeueBuild(ctx context.Context, buildID, holder string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE site_builds AS running
		SET status = CASE WHEN newer.id IS NULL THEN 'QUEUED' ELSE 'CANCELLED' END,
			failure_reason = CASE WHEN newer.id IS NULL THEN NULL ELSE $3 END,
			finished_at = CASE WHEN newer.id IS NULL THEN NULL ELSE NOW() END,
			started_at = NULL, lease_holder = NULL, lease_until = NULL
		FROM site_builds AS this
		LEFT JOIN LATERAL (SELECT id FROM site_builds WHERE kind = this.kind AND project_id = this.project_id
			AND (site_id = this.site_id OR app_id = this.app_id) AND status = 'QUEUED' LIMIT 1) AS newer ON TRUE
		WHERE running.id = $1 AND this.id = $1 AND running.status = 'RUNNING' AND running.lease_holder = $2`,
		buildID, holder, supersededReason)
	if err != nil {
		return fmt.Errorf("hand the build back to the queue: %w", err)
	}
	return nil
}
