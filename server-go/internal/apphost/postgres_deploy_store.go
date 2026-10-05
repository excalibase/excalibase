package apphost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// deployLockSpace is distinct from appLimitLockSpace (378) so a deploy and
// the project's app-slot count never wait on each other.
const deployLockSpace = 386

type PostgresDeployStore struct {
	db *sql.DB
}

func NewPostgresDeployStore(db *sql.DB) *PostgresDeployStore {
	return &PostgresDeployStore{db: db}
}

func (s *PostgresDeployStore) Create(deploy *Deploy) error {
	if deploy.Status != DeployStatusPending {
		return fmt.Errorf("a new deploy must be created %s, not %q", DeployStatusPending, deploy.Status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin deploy create transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
		deployLockSpace, deploy.AppID); err != nil {
		return fmt.Errorf("lock app deploy slot: %w", err)
	}
	if err := lockAppForDeploy(tx, deploy.ProjectID, deploy.AppID); err != nil {
		return err
	}

	if _, err := tx.Exec(
		`UPDATE app_deploys SET status = $3 WHERE app_id = $1 AND status IN ($2, $4)`,
		deploy.AppID, DeployStatusPending, DeployStatusSuperseded, DeployStatusRolling); err != nil {
		return fmt.Errorf("supersede earlier deploy: %w", err)
	}

	var maxRevision sql.NullInt64
	if err := tx.QueryRow(`SELECT max(revision) FROM app_deploys WHERE app_id = $1`, deploy.AppID).
		Scan(&maxRevision); err != nil {
		return fmt.Errorf("count app deploys: %w", err)
	}
	deploy.Revision = int(maxRevision.Int64) + 1

	spec, err := json.Marshal(deploy.Spec)
	if err != nil {
		return fmt.Errorf("marshal deploy spec: %w", err)
	}
	config, err := json.Marshal(deploy.Config)
	if err != nil {
		return fmt.Errorf("marshal deploy config: %w", err)
	}
	const q = `
INSERT INTO app_deploys (id, app_id, project_id, revision, image, spec, config, redeploy_of, status, created_by, created_at,
                         source, commit_sha, image_ref, digest)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	if _, err := tx.Exec(q, deploy.ID, deploy.AppID, deploy.ProjectID, deploy.Revision,
		deploy.Image, spec, config, nullIfEmpty(deploy.RedeployOf), deploy.Status, deploy.CreatedBy, deploy.CreatedAt,
		nullIfEmpty(deploy.Source), nullIfEmpty(deploy.CommitSHA), nullIfEmpty(deploy.ImageRef), nullIfEmpty(deploy.Digest)); err != nil {
		return fmt.Errorf("create deploy: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresDeployStore) RecordResize(resize *Deploy) error {
	if resize.Kind != DeployKindResize || resize.Status != DeployStatusSucceeded || resize.FinishedAt == nil {
		return fmt.Errorf("a resize is recorded as a finished %s entry", DeployKindResize)
	}
	spec, err := json.Marshal(resize.Spec)
	if err != nil {
		return fmt.Errorf("marshal resize spec: %w", err)
	}
	config, err := json.Marshal(resize.Config)
	if err != nil {
		return fmt.Errorf("marshal resize config: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin resize transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`, deployLockSpace, resize.AppID); err != nil {
		return fmt.Errorf("lock app deploy slot: %w", err)
	}
	if err := lockResumingApp(tx, resize.ProjectID, resize.AppID); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COALESCE(max(revision), 0) + 1 FROM app_deploys WHERE app_id = $1`, resize.AppID).
		Scan(&resize.Revision); err != nil {
		return fmt.Errorf("count app deploys: %w", err)
	}
	const q = `
INSERT INTO app_deploys (id, app_id, project_id, revision, image, spec, config, status, created_by, created_at, finished_at, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	if _, err := tx.Exec(q, resize.ID, resize.AppID, resize.ProjectID, resize.Revision, resize.Image, spec, config,
		resize.Status, resize.CreatedBy, resize.CreatedAt, resize.FinishedAt, resize.Kind); err != nil {
		return fmt.Errorf("record resize: %w", err)
	}
	// The recorded size follows the plan, not a developer's edit, so the version stays.
	if _, err := tx.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{tier}', to_jsonb($3::text)) WHERE project_id = $1 AND id = $2`,
		resize.ProjectID, resize.AppID, string(resize.Config.Tier)); err != nil {
		return fmt.Errorf("record app tier: %w", err)
	}
	return tx.Commit()
}

func lockResumingApp(tx *sql.Tx, projectID, appID string) error {
	var status string
	err := tx.QueryRow(`SELECT status FROM apps WHERE project_id = $1 AND id = $2 FOR UPDATE`, projectID, appID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppNotFound
	}
	if err != nil {
		return fmt.Errorf("read app for resize: %w", err)
	}
	if status != StatusResuming {
		return fmt.Errorf("%w: it is %s", ErrAppStatusConflict, status)
	}
	return nil
}

// lockAppForDeploy holds the app row for the rest of the transaction, so a
// pause, resume or deletion cannot start between this check and the insert.
func lockAppForDeploy(tx *sql.Tx, projectID, appID string) error {
	var status string
	err := tx.QueryRow(`SELECT status FROM apps WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		projectID, appID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppNotFound
	}
	if err != nil {
		return fmt.Errorf("read app for deploy: %w", err)
	}
	if IsBusy(status) {
		return fmt.Errorf("%w: it is %s", ErrAppBusy, status)
	}
	return nil
}

// nullIfEmpty lets an empty RedeployOf insert NULL rather than a value the
// foreign key to app_deploys(id) would reject.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Applies only from pending/rolling, so a late write is a silent no-op.
func (s *PostgresDeployStore) UpdateStatus(id, status, failureReason string, finishedAt *time.Time) error {
	_, err := s.db.Exec(
		`UPDATE app_deploys SET status = $2, failure_reason = NULLIF($3, ''), finished_at = $4
		 WHERE id = $1 AND status IN ($5, $6)`,
		id, status, failureReason, finishedAt, DeployStatusPending, DeployStatusRolling)
	if err != nil {
		return fmt.Errorf("update deploy status: %w", err)
	}
	return nil
}

// Finish leaves the app's version alone: the status is observed state, not a
// change to the record a developer's If-Match is checked against.
func (s *PostgresDeployStore) Finish(id, status, failureReason string, finishedAt time.Time, appStatus string) error {
	if appStatus != "" && !validStatuses[appStatus] {
		return fmt.Errorf("unknown app status: %q", appStatus)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin deploy finish transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	// The app row is locked before the deploy row, the order Transition takes them in.
	if _, err := tx.Exec(
		`SELECT 1 FROM apps WHERE (project_id, id) = (SELECT project_id, app_id FROM app_deploys WHERE id = $1) FOR UPDATE`,
		id); err != nil {
		return fmt.Errorf("lock app for deploy finish: %w", err)
	}

	var projectID, appID string
	err = tx.QueryRow(
		`UPDATE app_deploys SET status = $2, failure_reason = NULLIF($3, ''), finished_at = $4
		 WHERE id = $1 AND status IN ($5, $6) RETURNING project_id, app_id`,
		id, status, failureReason, finishedAt, DeployStatusPending, DeployStatusRolling).Scan(&projectID, &appID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finish deploy: %w", err)
	}
	if appStatus == "" {
		return tx.Commit()
	}
	if _, err := tx.Exec(
		`UPDATE apps SET status = $3, doc = jsonb_set(doc, '{status}', to_jsonb($3::text))
		 WHERE project_id = $1 AND id = $2`, projectID, appID, appStatus); err != nil {
		return fmt.Errorf("record app status: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresDeployStore) ListUnfinished() ([]*Deploy, error) {
	rows, err := s.db.Query(
		`SELECT `+deployColumns+`
		 FROM app_deploys WHERE status IN ($1, $2) ORDER BY created_at`, DeployStatusPending, DeployStatusRolling)
	if err != nil {
		return nil, fmt.Errorf("list unfinished deploys: %w", err)
	}
	defer rows.Close()
	out := make([]*Deploy, 0)
	for rows.Next() {
		deploy, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, deploy)
	}
	return out, rows.Err()
}

func (s *PostgresDeployStore) ListByApp(projectID, appID string, limit int) ([]*Deploy, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(appID); err != nil {
		return nil, err
	}
	q := `SELECT ` + deployColumns + `
	      FROM app_deploys WHERE project_id = $1 AND app_id = $2 ORDER BY revision DESC`
	args := []any{projectID, appID}
	if limit > 0 {
		q += " LIMIT $3"
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list deploys: %w", err)
	}
	defer rows.Close()

	out := make([]*Deploy, 0)
	for rows.Next() {
		deploy, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, deploy)
	}
	return out, rows.Err()
}

func (s *PostgresDeployStore) GetLatest(projectID, appID string) (*Deploy, error) {
	deploys, err := s.ListByApp(projectID, appID, 1)
	if err != nil {
		return nil, err
	}
	if len(deploys) == 0 {
		return nil, nil
	}
	return deploys[0], nil
}

// Get returns nil, not an error, when id belongs to another app or project or
// does not exist — the caller cannot tell those apart from the row alone, and
// should not need to.
func (s *PostgresDeployStore) Get(projectID, appID, id string) (*Deploy, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(appID); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(
		`SELECT `+deployColumns+`
		 FROM app_deploys WHERE id = $1 AND app_id = $2 AND project_id = $3`, id, appID, projectID)
	deploy, err := scanDeploy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return deploy, nil
}

const deployColumns = `id, app_id, project_id, revision, image, spec, config, redeploy_of, status, failure_reason,
	created_by, created_at, finished_at, kind, source, commit_sha, image_ref, digest`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeploy(row rowScanner) (*Deploy, error) {
	var d Deploy
	var spec, config []byte
	var failureReason, redeployOf, source, commitSHA, imageRef, digest sql.NullString
	var finishedAt sql.NullTime
	if err := row.Scan(&d.ID, &d.AppID, &d.ProjectID, &d.Revision, &d.Image, &spec, &config, &redeployOf,
		&d.Status, &failureReason, &d.CreatedBy, &d.CreatedAt, &finishedAt, &d.Kind,
		&source, &commitSHA, &imageRef, &digest); err != nil {
		return nil, fmt.Errorf("scan deploy: %w", err)
	}
	if err := json.Unmarshal(spec, &d.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal deploy spec: %w", err)
	}
	if err := json.Unmarshal(config, &d.Config); err != nil {
		return nil, fmt.Errorf("unmarshal deploy config: %w", err)
	}
	d.RedeployOf = redeployOf.String
	d.Source, d.CommitSHA, d.ImageRef, d.Digest = source.String, commitSHA.String, imageRef.String, digest.String
	d.FailureReason = failureReason.String
	if finishedAt.Valid {
		d.FinishedAt = &finishedAt.Time
	}
	return &d, nil
}
