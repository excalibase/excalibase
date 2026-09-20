package apphost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// appLimitLockSpace namespaces the per-project advisory lock the one-app rule
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
func (s *PostgresAppStore) Create(app *App) error {
	now := time.Now().UTC()
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
	if held >= MaxAppsPerProject {
		return ErrAppLimitReached
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

// Update replaces the stored record. The version and created_at the caller
// sends are ignored: both are read back from the row inside the transaction,
// so a stale copy of the record cannot rewrite the app's history.
func (s *PostgresAppStore) Update(app *App) error {
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
	err = tx.QueryRow(
		`SELECT version, created_at FROM apps WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		app.ProjectID, app.ID).Scan(&version, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppNotFound
	}
	if err != nil {
		return fmt.Errorf("read app for update: %w", err)
	}

	app.Version = version + 1
	app.CreatedAt = createdAt
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

// Delete removes the app and frees the project's app slot.
func (s *PostgresAppStore) Delete(projectID, id string) error {
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`DELETE FROM apps WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	if removed == 0 {
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
