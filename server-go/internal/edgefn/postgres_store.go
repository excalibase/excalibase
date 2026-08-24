package edgefn

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PostgresFunctionStore persists functions in the platform store (edge_functions).
// Replaces the filesystem layout for cloud mode, where STORAGE_PATH is an
// ephemeral emptyDir — see EXC-333. The whole Function is stored as a JSON
// document (same bytes the filesystem store wrote), so no field can silently
// drop as the struct grows; `files` inside it is the source of truth and the
// deploy bundle is derived from it, letting a cold runtime be replayed anytime.
type PostgresFunctionStore struct {
	db *sql.DB
}

func NewPostgresFunctionStore(db *sql.DB) *PostgresFunctionStore {
	return &PostgresFunctionStore{db: db}
}

// Save upserts the function, preserving created_at and incrementing version on
// replace — matching the filesystem store's semantics (version starts at 1).
func (s *PostgresFunctionStore) Save(fn *Function) error {
	// Validate bundles the function, so the project's shared modules must be in
	// scope or a `_shared/…` import would fail to resolve (EXC-334).
	shared, err := s.SharedFiles(fn.ProjectID)
	if err != nil {
		return fmt.Errorf("load shared files: %w", err)
	}
	if err := fn.ValidateWith(shared); err != nil {
		return err
	}

	// Version/timestamps are decided by the DB so concurrent saves can't race
	// to the same version. Read the current row first for created_at parity.
	now := time.Now()
	fn.UpdatedAt = now

	const q = `
INSERT INTO edge_functions (project_id, id, version, active, doc, created_at, updated_at)
VALUES ($1, $2, 1, $3, $4, $5, $5)
ON CONFLICT (project_id, id) DO UPDATE SET
    version    = edge_functions.version + 1,
    active     = EXCLUDED.active,
    doc        = EXCLUDED.doc,
    updated_at = EXCLUDED.updated_at
RETURNING version, created_at`

	// Marshal a first time to reserve the row, then rewrite the doc with the
	// authoritative version/created_at so the stored document matches the row.
	blob, err := json.Marshal(fn)
	if err != nil {
		return fmt.Errorf("marshal function: %w", err)
	}
	var version int
	var createdAt time.Time
	if err := s.db.QueryRow(q, fn.ProjectID, fn.ID, fn.Active, blob, now).
		Scan(&version, &createdAt); err != nil {
		return fmt.Errorf("save function: %w", err)
	}

	fn.Version = version
	fn.CreatedAt = createdAt
	if blob, err = json.Marshal(fn); err != nil {
		return fmt.Errorf("marshal function: %w", err)
	}
	if _, err := s.db.Exec(
		`UPDATE edge_functions SET doc = $3 WHERE project_id = $1 AND id = $2`,
		fn.ProjectID, fn.ID, blob); err != nil {
		return fmt.Errorf("persist function version: %w", err)
	}
	return nil
}

// Get returns nil, nil when the function does not exist (filesystem parity).
func (s *PostgresFunctionStore) Get(projectID, id string) (*Function, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	var blob []byte
	err := s.db.QueryRow(
		`SELECT doc FROM edge_functions WHERE project_id = $1 AND id = $2`,
		projectID, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get function: %w", err)
	}
	return decodeFunction(blob)
}

func (s *PostgresFunctionStore) List(projectID string) ([]*Function, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT doc FROM edge_functions WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	defer rows.Close()

	out := make([]*Function, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("scan function: %w", err)
		}
		fn, err := decodeFunction(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, fn)
	}
	return out, rows.Err()
}

func (s *PostgresFunctionStore) Delete(projectID, id string) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`DELETE FROM edge_functions WHERE project_id = $1 AND id = $2`, projectID, id); err != nil {
		return fmt.Errorf("delete function: %w", err)
	}
	return nil
}

// --- shared files (EXC-334) ---

func (s *PostgresFunctionStore) SharedFiles(projectID string) ([]File, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT path, content FROM edge_shared_files WHERE project_id = $1 ORDER BY path`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list shared files: %w", err)
	}
	defer rows.Close()

	out := make([]File, 0)
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Content); err != nil {
			return nil, fmt.Errorf("scan shared file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresFunctionStore) PutSharedFile(projectID string, file File) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateSharedPath(file.Path); err != nil {
		return err
	}
	const q = `
INSERT INTO edge_shared_files (project_id, path, content, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (project_id, path) DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()`
	if _, err := s.db.Exec(q, projectID, file.Path, file.Content); err != nil {
		return fmt.Errorf("put shared file: %w", err)
	}
	return nil
}

func (s *PostgresFunctionStore) DeleteSharedFile(projectID, path string) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`DELETE FROM edge_shared_files WHERE project_id = $1 AND path = $2`,
		projectID, path); err != nil {
		return fmt.Errorf("delete shared file: %w", err)
	}
	return nil
}

func decodeFunction(blob []byte) (*Function, error) {
	var fn Function
	if err := json.Unmarshal(blob, &fn); err != nil {
		return nil, fmt.Errorf("unmarshal function: %w", err)
	}
	return &fn, nil
}
