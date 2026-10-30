package sitebuilds

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// dispatchLock serialises claims across replicas, so the slot count read and the claim are one step.
const dispatchLock = 354_000_001

const supersededReason = "superseded by a newer commit before it started"

// The three build queries return the columns scanBuild reads, in its order; they are spelled out so each query stays one literal.
const (
	claimBuildSQL = `UPDATE site_builds SET status = 'RUNNING', started_at = NOW(), lease_holder = $2, lease_until = $3
		WHERE id = $1 RETURNING id, site_id, project_id, status, trigger, commit_sha, COALESCE(commit_message, ''), settings,
		COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds, project_concurrency, COALESCE(deploy_id, ''),
		COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by, created_at, started_at, finished_at, COALESCE(preview_id, '')`
	listBuildsSQL = `SELECT id, site_id, project_id, status, trigger, commit_sha, COALESCE(commit_message, ''), settings,
		COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds, project_concurrency, COALESCE(deploy_id, ''),
		COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by, created_at, started_at, finished_at, COALESCE(preview_id, '')
		FROM site_builds WHERE project_id = $1 AND site_id = $2 ORDER BY created_at DESC LIMIT $3`
	getBuildSQL = `SELECT id, site_id, project_id, status, trigger, commit_sha, COALESCE(commit_message, ''), settings,
		COALESCE(delivery_id, ''), cpu_millis, memory, disk, timeout_seconds, project_concurrency, COALESCE(deploy_id, ''),
		COALESCE(failure_kind, ''), COALESCE(failure_reason, ''), requested_by, created_at, started_at, finished_at, COALESCE(preview_id, ''), COALESCE(log, '')
		FROM site_builds WHERE id = $1 AND project_id = $2 AND site_id = $3`
)

// Enqueue writes build as given: QUEUED supersedes the site's queued ones; a
// build refused before it could queue is written FAILED, so its push is not lost silently.
func (s *PostgresStore) Enqueue(ctx context.Context, build *Build, keep int) (bool, error) {
	snapshot, err := json.Marshal(buildSnapshot{Settings: build.Settings, Provider: build.Provider,
		InstallationID: build.InstallationID, RepositoryID: build.RepositoryID, Repository: build.Repository, Preview: build.Preview})
	if err != nil {
		return false, fmt.Errorf("encode the build's settings: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if build.DeliveryID != "" {
		var seen bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM site_builds WHERE site_id = $1 AND delivery_id = $2)`,
			build.SiteID, build.DeliveryID).Scan(&seen); err != nil {
			return false, fmt.Errorf("check the delivery: %w", err)
		}
		if seen {
			return false, nil
		}
	}
	if build.Status == StatusQueued {
		// A preview build supersedes only its own preview's; a production build only production's.
		if _, err := tx.ExecContext(ctx, `UPDATE site_builds SET status = 'CANCELLED', failure_reason = $2, finished_at = NOW()
			WHERE site_id = $1 AND status = 'QUEUED' AND preview_id IS NOT DISTINCT FROM NULLIF($3, '')`,
			build.SiteID, supersededReason, build.PreviewID); err != nil {
			return false, fmt.Errorf("supersede queued builds: %w", err)
		}
	}
	limits := build.Limits
	result, err := tx.ExecContext(ctx, `INSERT INTO site_builds (id, site_id, project_id, status, trigger, commit_sha, commit_message,
		settings, delivery_id, cpu_millis, memory, disk, timeout_seconds, project_concurrency, requested_by, created_at,
		failure_kind, failure_reason, finished_at, preview_id)
		SELECT $1, id, project_id, $16, $4, $5, NULLIF($6, ''), $7, NULLIF($8, ''), $9, $10, $11, $12, $13, $14, $15,
		NULLIF($17, ''), NULLIF($18, ''), CASE WHEN $16 = 'QUEUED' THEN NULL ELSE NOW() END, NULLIF($19, '')
		FROM sites WHERE id = $2 AND project_id = $3`,
		build.ID, build.SiteID, build.ProjectID, build.Trigger, build.CommitSHA, build.CommitMessage, snapshot, build.DeliveryID,
		limits.CPUMillis, limits.Memory, limits.Disk, limits.TimeoutSeconds, limits.ProjectConcurrency, build.RequestedBy, build.CreatedAt,
		build.Status, string(build.FailureKind), build.FailureReason, build.PreviewID)
	if err != nil {
		return false, fmt.Errorf("queue the build: %w", err)
	}
	if err := requireTouched(result, ErrNotConnected); err != nil {
		return false, err
	}
	// Production and each preview keep their own history, so a busy pull request never pushes out the site's.
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_builds WHERE site_id = $1 AND preview_id IS NOT DISTINCT FROM NULLIF($3, '')
		AND status IN ('SUCCEEDED', 'FAILED', 'CANCELLED')
		AND id NOT IN (SELECT id FROM site_builds WHERE site_id = $1 AND preview_id IS NOT DISTINCT FROM NULLIF($3, '') ORDER BY created_at DESC LIMIT $2)`,
		build.SiteID, keep, build.PreviewID); err != nil {
		return false, fmt.Errorf("trim the build history: %w", err)
	}
	return true, tx.Commit()
}

func (s *PostgresStore) ClaimNext(ctx context.Context, slots int, holder string, leaseUntil time.Time) (*Build, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, dispatchLock); err != nil {
		return nil, fmt.Errorf("take the dispatch lock: %w", err)
	}
	var id string
	// One build at a time per site and scope (production, or one preview). Production builds count
	// against the plan's concurrency; previews against PreviewsRunningPerProject on their own, so a
	// flood of pull requests holds at most that many install slots and never a production build back.
	err = tx.QueryRowContext(ctx, `SELECT queued.id FROM site_builds queued
		WHERE queued.status = 'QUEUED'
		  AND (SELECT COUNT(*) FROM site_builds WHERE status = 'RUNNING') < $1
		  AND NOT EXISTS (SELECT 1 FROM site_builds WHERE status = 'RUNNING' AND site_id = queued.site_id
		    AND preview_id IS NOT DISTINCT FROM queued.preview_id)
		  AND (SELECT COUNT(*) FROM site_builds WHERE status = 'RUNNING' AND project_id = queued.project_id
		    AND (preview_id IS NULL) = (queued.preview_id IS NULL))
		    < CASE WHEN queued.preview_id IS NULL THEN queued.project_concurrency ELSE $2 END
		ORDER BY queued.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, slots, PreviewsRunningPerProject).Scan(&id)
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
	err := row.Scan(&build.ID, &build.SiteID, &build.ProjectID, &build.Status, &build.Trigger, &build.CommitSHA, &build.CommitMessage,
		&snapshot, &build.DeliveryID, &limits.CPUMillis, &limits.Memory, &limits.Disk, &limits.TimeoutSeconds, &limits.ProjectConcurrency,
		&build.DeployID, &build.FailureKind, &build.FailureReason, &build.RequestedBy, &build.CreatedAt, &started, &finished, &build.PreviewID)
	if err != nil {
		return nil, err
	}
	var decoded buildSnapshot
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		return nil, fmt.Errorf("decode the build's settings: %w", err)
	}
	build.Settings, build.Provider, build.InstallationID, build.RepositoryID, build.Repository, build.Preview =
		decoded.Settings, decoded.Provider, decoded.InstallationID, decoded.RepositoryID, decoded.Repository, decoded.Preview
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

func (s *PostgresStore) ExpireLostBuilds(ctx context.Context, now time.Time, reason string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE site_builds SET status = 'FAILED', failure_kind = 'platform', failure_reason = $2,
		finished_at = NOW(), lease_holder = NULL, lease_until = NULL
		WHERE status = 'RUNNING' AND lease_until < $1 RETURNING id`, now, reason)
	if err != nil {
		return nil, fmt.Errorf("expire lost builds: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) ListBuilds(ctx context.Context, projectID, siteID string, limit int) ([]*Build, error) {
	rows, err := s.db.QueryContext(ctx, listBuildsSQL, projectID, siteID, limit)
	if err != nil {
		return nil, fmt.Errorf("list the site's builds: %w", err)
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

func (s *PostgresStore) GetBuild(ctx context.Context, projectID, siteID, id string) (*Build, error) {
	row := s.db.QueryRowContext(ctx, getBuildSQL, id, projectID, siteID)
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

func (s *PostgresStore) DeliveryQueued(ctx context.Context, siteID, deliveryID string) (bool, error) {
	var queued bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM site_builds WHERE site_id = $1 AND delivery_id = $2)`,
		siteID, deliveryID).Scan(&queued)
	if err != nil {
		return false, fmt.Errorf("read the site's delivery: %w", err)
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
		LEFT JOIN LATERAL (SELECT id FROM site_builds WHERE site_id = this.site_id AND status = 'QUEUED'
			AND preview_id IS NOT DISTINCT FROM this.preview_id LIMIT 1) AS newer ON TRUE
		WHERE running.id = $1 AND this.id = $1 AND running.status = 'RUNNING' AND running.lease_holder = $2`,
		buildID, holder, supersededReason)
	if err != nil {
		return fmt.Errorf("hand the build back to the queue: %w", err)
	}
	return nil
}
