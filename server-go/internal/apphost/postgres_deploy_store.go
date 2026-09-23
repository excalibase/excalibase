package apphost

import (
	"database/sql"
	"encoding/json"
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
	const q = `
INSERT INTO app_deploys (id, app_id, project_id, revision, image, spec, status, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := tx.Exec(q, deploy.ID, deploy.AppID, deploy.ProjectID, deploy.Revision,
		deploy.Image, spec, deploy.Status, deploy.CreatedBy, deploy.CreatedAt); err != nil {
		return fmt.Errorf("create deploy: %w", err)
	}
	return tx.Commit()
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

func (s *PostgresDeployStore) ListByApp(projectID, appID string, limit int) ([]*Deploy, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(appID); err != nil {
		return nil, err
	}
	q := `SELECT id, app_id, project_id, revision, image, spec, status, failure_reason, created_by, created_at, finished_at
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeploy(row rowScanner) (*Deploy, error) {
	var d Deploy
	var spec []byte
	var failureReason sql.NullString
	var finishedAt sql.NullTime
	if err := row.Scan(&d.ID, &d.AppID, &d.ProjectID, &d.Revision, &d.Image, &spec,
		&d.Status, &failureReason, &d.CreatedBy, &d.CreatedAt, &finishedAt); err != nil {
		return nil, fmt.Errorf("scan deploy: %w", err)
	}
	if err := json.Unmarshal(spec, &d.Spec); err != nil {
		return nil, fmt.Errorf("unmarshal deploy spec: %w", err)
	}
	d.FailureReason = failureReason.String
	if finishedAt.Valid {
		d.FinishedAt = &finishedAt.Time
	}
	return &d, nil
}
