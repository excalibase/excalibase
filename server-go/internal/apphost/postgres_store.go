package apphost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lib/pq"
)

// appLimitLockSpace namespaces the per-project advisory lock the app limit
// is enforced under. Postgres keeps two-integer advisory locks in a key space
// of their own, separate from the single-bigint locks the schedulers lead on,
// so this cannot collide with them. It differs from the org project-limit
// space so the two limits never serialize against each other.
const appLimitLockSpace = 378

// uniqueViolation is the SQLSTATE a duplicate app name raises.
const uniqueViolation = "23505"

// PostgresAppStore persists apps in the platform store (the `apps` table).
//
// The whole App is stored as a JSON document, the way the edge-function record
// is: the struct carries optional fields (env, health check path, resolved
// digest) and more will arrive with the deploy lifecycle, so a
// column-per-field schema would silently drop whatever was added last. The
// columns beside the document are extracted only for keys, ordering and the
// limit count.
type PostgresAppStore struct {
	db *sql.DB
}

func NewPostgresAppStore(db *sql.DB) *PostgresAppStore {
	return &PostgresAppStore{db: db}
}

// Create inserts the app only while the project has a free app slot.
//
// Counting and inserting in one transaction is not enough on its own: at READ
// COMMITTED neither transaction sees the other's uncommitted row, so two
// concurrent creates would both count the same free slot and both commit. The
// transaction therefore takes an advisory lock keyed on the project first. The
// lock is held to commit (pg_advisory_xact_lock releases at end of
// transaction, on rollback too), so the second create blocks until the first
// row is committed and visible, then counts it. Locking on a value rather than
// a row means a project with no app yet is serialized just the same.
func (s *PostgresAppStore) Create(app *App, maxApps int) error {
	now := time.Now().UTC()
	app.LifecycleFailure = nil
	app.Version = 1
	app.CreatedAt = now
	app.UpdatedAt = now
	if err := app.Validate(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin app create transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
		appLimitLockSpace, app.ProjectID); err != nil {
		return fmt.Errorf("lock project app slots: %w", err)
	}

	var held int
	if err := tx.QueryRow(`SELECT count(*) FROM apps WHERE project_id = $1`, app.ProjectID).
		Scan(&held); err != nil {
		return fmt.Errorf("count project apps: %w", err)
	}
	if held >= maxApps {
		return AppLimitError{Limit: maxApps}
	}

	blob, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshal app: %w", err)
	}
	const q = `
INSERT INTO apps (project_id, id, name, status, version, doc, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`
	if _, err := tx.Exec(q, app.ProjectID, app.ID, app.Name, app.Status, app.Version, blob, now); err != nil {
		return createError(err)
	}
	return tx.Commit()
}

// createError maps a duplicate key onto the reason the caller can act on: a
// name already used, or the same app id inserted twice.
func createError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation {
		if pqErr.Constraint == "apps_pkey" {
			return fmt.Errorf("app id already exists: %w", ErrAppNameTaken)
		}
		return ErrAppNameTaken
	}
	return fmt.Errorf("create app: %w", err)
}

// Get returns nil, nil when the project holds no such app.
func (s *PostgresAppStore) Get(projectID, id string) (*App, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	var blob []byte
	err := s.db.QueryRow(
		`SELECT doc FROM apps WHERE project_id = $1 AND id = $2`, projectID, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get app: %w", err)
	}
	return decodeApp(blob)
}

// List returns the project's apps, ordered by name so the response is stable.
func (s *PostgresAppStore) List(projectID string) ([]*App, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT doc FROM apps WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()

	out := make([]*App, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		app, err := decodeApp(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// Update replaces the stored record, but only while the row still holds the
// version the caller read.
//
// The version is compared inside the transaction, under the row lock, so the
// comparison and the write cannot be separated: two developers who read the
// same app and both patch it do not silently lose one of the changes — the
// second is told the app moved. created_at is read back from the row for the
// same reason, so a stale copy cannot rewrite the app's history, and so is the
// status, which a deploy may have moved since the caller read the app.
func (s *PostgresAppStore) Update(app *App, expectedVersion int) error {
	if err := app.Validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin app update transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	var version int
	var createdAt time.Time
	var status string
	var failure []byte
	err = tx.QueryRow(
		`SELECT version, created_at, status, doc->'lifecycleFailure' FROM apps WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		app.ProjectID, app.ID).Scan(&version, &createdAt, &status, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppNotFound
	}
	if err != nil {
		return fmt.Errorf("read app for update: %w", err)
	}

	if status == StatusDeleting {
		return ErrAppBusy
	}
	if version != expectedVersion {
		return fmt.Errorf("%w: it is at version %d, not %d", ErrAppVersionConflict, version, expectedVersion)
	}
	app.Version = version + 1
	app.CreatedAt = createdAt
	// Only a finished deploy moves the status; the caller's copy may predate it.
	app.Status = status
	// Only RecordLifecycleFailure writes the failure.
	app.LifecycleFailure = nil
	if len(failure) > 0 {
		if err := json.Unmarshal(failure, &app.LifecycleFailure); err != nil {
			return fmt.Errorf("read app lifecycle failure: %w", err)
		}
	}
	app.UpdatedAt = time.Now().UTC()
	blob, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshal app: %w", err)
	}

	const q = `
UPDATE apps SET name = $3, status = $4, version = $5, doc = $6, updated_at = $7
WHERE project_id = $1 AND id = $2`
	if _, err := tx.Exec(q, app.ProjectID, app.ID, app.Name, app.Status, app.Version, blob, app.UpdatedAt); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation {
			return ErrAppNameTaken
		}
		return fmt.Errorf("update app: %w", err)
	}
	return tx.Commit()
}

// Transition locks the app row before the deploy rows, the same order Finish
// and the deploy store's Create take them in.
func (s *PostgresAppStore) Transition(projectID, id string, from []string, to string) (*App, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if !validStatuses[to] {
		return nil, fmt.Errorf("unknown app status: %q", to)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin app transition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	var blob []byte
	var status string
	err = tx.QueryRow(`SELECT doc, status FROM apps WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		projectID, id).Scan(&blob, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read app for transition: %w", err)
	}
	if !slices.Contains(from, status) {
		return nil, fmt.Errorf("%w: it is %s", ErrAppStatusConflict, status)
	}
	if _, err := tx.Exec(
		`UPDATE apps SET status = $3, doc = jsonb_set(doc, '{status}', to_jsonb($3::text))
		 WHERE project_id = $1 AND id = $2`, projectID, id, to); err != nil {
		return nil, fmt.Errorf("record app status: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE app_deploys SET status = $3 WHERE project_id = $1 AND app_id = $2 AND status IN ($4, $5)`,
		projectID, id, DeployStatusSuperseded, DeployStatusPending, DeployStatusRolling); err != nil {
		return nil, fmt.Errorf("supersede unfinished deploys: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit app transition: %w", err)
	}
	app, err := decodeApp(blob)
	if err != nil {
		return nil, err
	}
	app.Status = to
	return app, nil
}

// Delete removes the row, and its deploy history with it, only after a
// teardown has claimed the app; a row in any other status still describes a
// workload that may be running.
func (s *PostgresAppStore) Delete(projectID, id string) error {
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	var status string
	err := s.db.QueryRow(
		`WITH target AS (SELECT status FROM apps WHERE project_id = $1 AND id = $2),
		      removed AS (DELETE FROM apps WHERE project_id = $1 AND id = $2 AND status = $3 RETURNING status)
		 SELECT COALESCE((SELECT status FROM removed), (SELECT status FROM target), '')`,
		projectID, id, StatusDeleting).Scan(&status)
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	switch status {
	case "":
		return ErrAppNotFound
	case StatusDeleting:
		return nil
	default:
		return fmt.Errorf("%w: it is %s, not being deleted", ErrAppStatusConflict, status)
	}
}

// RecordLifecycleFailure writes or clears the failure inside the stored record
// without touching its version: it is an outcome, not an edit.
func (s *PostgresAppStore) RecordLifecycleFailure(projectID, id string, failure *LifecycleFailure) error {
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	var res sql.Result
	var err error
	if failure == nil {
		res, err = s.db.Exec(`UPDATE apps SET doc = doc - 'lifecycleFailure' WHERE project_id = $1 AND id = $2`,
			projectID, id)
	} else {
		blob, merr := json.Marshal(failure)
		if merr != nil {
			return fmt.Errorf("marshal app lifecycle failure: %w", merr)
		}
		res, err = s.db.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{lifecycleFailure}', $3::jsonb) WHERE project_id = $1 AND id = $2`,
			projectID, id, string(blob))
	}
	if err != nil {
		return fmt.Errorf("record app lifecycle failure: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("record app lifecycle failure: %w", err)
	} else if n == 0 {
		return ErrAppNotFound
	}
	return nil
}

// PurgeProjectApps removes every app of a project being deleted, with its
// deploy history, once the namespace its workloads ran in is gone.
func (s *PostgresAppStore) PurgeProjectApps(projectID string) (int, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`DELETE FROM apps WHERE project_id = $1`, projectID)
	if err != nil {
		return 0, fmt.Errorf("purge project apps: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge project apps: %w", err)
	}
	return int(removed), nil
}

func (s *PostgresAppStore) ListAutoDeploy() ([]*App, error) {
	rows, err := s.db.Query(`SELECT doc FROM apps WHERE doc @> '{"autoDeploy": true}' ORDER BY project_id, name`)
	if err != nil {
		return nil, fmt.Errorf("list auto-deploy apps: %w", err)
	}
	defer rows.Close()
	out := make([]*App, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		app, err := decodeApp(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (s *PostgresAppStore) RecordImageWatch(projectID, id string, watch ImageWatch) error {
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	blob, err := json.Marshal(watch)
	if err != nil {
		return fmt.Errorf("marshal image watch: %w", err)
	}
	res, err := s.db.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{imageWatch}', $3::jsonb) WHERE project_id = $1 AND id = $2`,
		projectID, id, string(blob))
	if err != nil {
		return fmt.Errorf("record image watch: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("record image watch: %w", err)
	} else if n == 0 {
		return ErrAppNotFound
	}
	return nil
}

func decodeApp(blob []byte) (*App, error) {
	var app App
	if err := json.Unmarshal(blob, &app); err != nil {
		return nil, fmt.Errorf("unmarshal app: %w", err)
	}
	return &app, nil
}
