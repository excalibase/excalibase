package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

const foreignKeyViolation = "23503"

// PermissionStore implements storage.PermissionStore over api_permissions,
// tracked_functions, function_permissions and permission_versions (000065).
type PermissionStore struct{ s *Store }

func NewPermissions(s *Store) *PermissionStore { return &PermissionStore{s: s} }

// Permissions on *Store satisfies storage.PlatformStore.Permissions.
func (s *Store) Permissions() storage.PermissionStore { return NewPermissions(s) }

func (p *PermissionStore) withTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	tx, err := p.s.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}

// bumpVersion is part of every write, inside the write's transaction.
func bumpVersion(ctx context.Context, tx *sql.Tx, projectID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO permission_versions (project_id, version) VALUES ($1, 1)
		ON CONFLICT (project_id) DO UPDATE SET version = permission_versions.version + 1`, projectID)
	return err
}

func (p *PermissionStore) PutPermission(ctx context.Context, perm domain.TablePermission) (bool, error) {
	var created bool
	err := p.withTx(ctx, nil, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO api_permissions (project_id, table_key, role_name, operation, definition)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, table_key, role_name, operation) DO UPDATE SET
				definition = EXCLUDED.definition, updated_at = NOW()
			RETURNING (xmax = 0)`,
			perm.ProjectID, perm.Table, perm.Role, perm.Operation, []byte(perm.Definition)).Scan(&created); err != nil {
			return err
		}
		return bumpVersion(ctx, tx, perm.ProjectID)
	})
	return created, err
}

func (p *PermissionStore) DeletePermission(ctx context.Context, projectID, table, role, operation string) error {
	return p.deleteOne(ctx, projectID, storage.ErrPermissionNotFound, `
		DELETE FROM api_permissions
		WHERE project_id = $1 AND table_key = $2 AND role_name = $3 AND operation = $4`,
		projectID, table, role, operation)
}

func (p *PermissionStore) TrackFunction(ctx context.Context, fn domain.TrackedFunction) error {
	return p.withTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tracked_functions (project_id, function_key, exposed_as, infer_permissions, session_argument)
			VALUES ($1, $2, $3, $4, $5)`,
			fn.ProjectID, fn.Function, fn.ExposedAs, fn.InferPermissions, fn.SessionArgument)
		if isPQCode(err, uniqueViolation) {
			return storage.ErrFunctionAlreadyTracked
		}
		if err != nil {
			return err
		}
		return bumpVersion(ctx, tx, fn.ProjectID)
	})
}

// UntrackFunction relies on function_permissions' foreign key cascading, so
// the function and every role's permission to call it go in one statement.
func (p *PermissionStore) UntrackFunction(ctx context.Context, projectID, function string) error {
	return p.deleteOne(ctx, projectID, storage.ErrFunctionNotTracked,
		`DELETE FROM tracked_functions WHERE project_id = $1 AND function_key = $2`, projectID, function)
}

func (p *PermissionStore) PutFunctionPermission(ctx context.Context, projectID, function, role string) error {
	return p.withTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO function_permissions (project_id, function_key, role_name) VALUES ($1, $2, $3)
			ON CONFLICT (project_id, function_key, role_name) DO NOTHING`, projectID, function, role)
		if isPQCode(err, foreignKeyViolation) {
			return storage.ErrFunctionNotTracked
		}
		if err != nil {
			return err
		}
		return bumpVersion(ctx, tx, projectID)
	})
}

func (p *PermissionStore) DeleteFunctionPermission(ctx context.Context, projectID, function, role string) error {
	return p.deleteOne(ctx, projectID, storage.ErrFunctionPermissionNotFound, `
		DELETE FROM function_permissions WHERE project_id = $1 AND function_key = $2 AND role_name = $3`,
		projectID, function, role)
}

// deleteOne runs a delete scoped to the project, answers notFound when it
// matched nothing, and bumps the version otherwise.
func (p *PermissionStore) deleteOne(ctx context.Context, projectID string, notFound error, query string, args ...any) error {
	return p.withTx(ctx, nil, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return notFound
		}
		return bumpVersion(ctx, tx, projectID)
	})
}

func isPQCode(err error, code string) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == code
}

// Document reads the three tables and the version in one repeatable-read
// snapshot, so the version always describes exactly the rows returned.
func (p *PermissionStore) Document(ctx context.Context, projectID string) (*domain.PermissionDocument, error) {
	doc := &domain.PermissionDocument{
		ProjectID: projectID, Tables: []domain.TablePermissions{},
		Functions: []domain.TrackedFunction{}, FunctionPermissions: []domain.FunctionPermission{},
	}
	err := p.withTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE((SELECT version FROM permission_versions WHERE project_id = $1), 0)`,
			projectID).Scan(&doc.Version); err != nil {
			return fmt.Errorf("read permission version: %w", err)
		}
		if err := readTablePermissions(ctx, tx, doc); err != nil {
			return err
		}
		if err := readTrackedFunctions(ctx, tx, doc); err != nil {
			return err
		}
		return readFunctionPermissions(ctx, tx, doc)
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func readTablePermissions(ctx context.Context, tx *sql.Tx, doc *domain.PermissionDocument) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT table_key, role_name, operation, definition FROM api_permissions
		WHERE project_id = $1 ORDER BY table_key, role_name, operation`, doc.ProjectID)
	if err != nil {
		return fmt.Errorf("query api_permissions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, role, operation string
		var definition []byte
		if err := rows.Scan(&table, &role, &operation, &definition); err != nil {
			return fmt.Errorf("scan api_permissions: %w", err)
		}
		entry := tableEntry(doc, table, role)
		setOperation(entry, operation, json.RawMessage(definition))
	}
	return rows.Err()
}

// tableEntry returns the (table, role) entry, appending it when the rows,
// ordered by table and role, reach a new pair.
func tableEntry(doc *domain.PermissionDocument, table, role string) *domain.TablePermissions {
	if n := len(doc.Tables); n > 0 && doc.Tables[n-1].Table == table && doc.Tables[n-1].Role == role {
		return &doc.Tables[n-1]
	}
	doc.Tables = append(doc.Tables, domain.TablePermissions{Table: table, Role: role})
	return &doc.Tables[len(doc.Tables)-1]
}

func setOperation(entry *domain.TablePermissions, operation string, definition json.RawMessage) {
	switch operation {
	case domain.PermissionSelect:
		entry.Select = definition
	case domain.PermissionInsert:
		entry.Insert = definition
	case domain.PermissionUpdate:
		entry.Update = definition
	case domain.PermissionDelete:
		entry.Delete = definition
	}
}

func readTrackedFunctions(ctx context.Context, tx *sql.Tx, doc *domain.PermissionDocument) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT function_key, exposed_as, infer_permissions, session_argument FROM tracked_functions
		WHERE project_id = $1 ORDER BY function_key`, doc.ProjectID)
	if err != nil {
		return fmt.Errorf("query tracked_functions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		fn := domain.TrackedFunction{ProjectID: doc.ProjectID}
		var sessionArgument sql.NullString
		if err := rows.Scan(&fn.Function, &fn.ExposedAs, &fn.InferPermissions, &sessionArgument); err != nil {
			return fmt.Errorf("scan tracked_functions: %w", err)
		}
		if sessionArgument.Valid {
			fn.SessionArgument = &sessionArgument.String
		}
		doc.Functions = append(doc.Functions, fn)
	}
	return rows.Err()
}

func readFunctionPermissions(ctx context.Context, tx *sql.Tx, doc *domain.PermissionDocument) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT function_key, role_name FROM function_permissions
		WHERE project_id = $1 ORDER BY function_key, role_name`, doc.ProjectID)
	if err != nil {
		return fmt.Errorf("query function_permissions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fp domain.FunctionPermission
		if err := rows.Scan(&fp.Function, &fp.Role); err != nil {
			return fmt.Errorf("scan function_permissions: %w", err)
		}
		doc.FunctionPermissions = append(doc.FunctionPermissions, fp)
	}
	return rows.Err()
}

// LegacyPending lists existing projects with legacy rows and no marker.
func (p *PermissionStore) LegacyPending(ctx context.Context) ([]string, error) {
	rows, err := p.s.db.QueryContext(ctx, `
		SELECT legacy.project_id FROM (
			SELECT project_id FROM table_grants
			UNION SELECT project_id FROM rls_policies
			UNION SELECT project_id FROM column_policies
		) legacy
		JOIN database_instances d ON d.project_id = legacy.project_id
		WHERE NOT EXISTS (SELECT 1 FROM legacy_permissions_migrated m WHERE m.project_id = legacy.project_id)
		ORDER BY legacy.project_id`)
	if err != nil {
		return nil, fmt.Errorf("query legacy projects: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		out = append(out, projectID)
	}
	return out, rows.Err()
}

// ImportLegacy never replaces what the new API already holds: a permission,
// tracked function or function permission written since wins.
func (p *PermissionStore) ImportLegacy(ctx context.Context, projectID string, set domain.LegacyPermissionImport) error {
	return p.withTx(ctx, nil, func(tx *sql.Tx) error {
		if err := importPermissions(ctx, tx, projectID, set); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO legacy_permissions_migrated (project_id) VALUES ($1)
			ON CONFLICT (project_id) DO NOTHING`, projectID); err != nil {
			return fmt.Errorf("mark legacy permissions migrated: %w", err)
		}
		return bumpVersion(ctx, tx, projectID)
	})
}

func importPermissions(ctx context.Context, tx *sql.Tx, projectID string, set domain.LegacyPermissionImport) error {
	for _, perm := range set.Permissions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_permissions (project_id, table_key, role_name, operation, definition)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, table_key, role_name, operation) DO NOTHING`,
			projectID, perm.Table, perm.Role, perm.Operation, []byte(perm.Definition)); err != nil {
			return fmt.Errorf("import permission %s/%s/%s: %w", perm.Table, perm.Role, perm.Operation, err)
		}
	}
	for _, fn := range set.Functions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tracked_functions (project_id, function_key, exposed_as, infer_permissions, session_argument)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, function_key) DO NOTHING`,
			projectID, fn.Function, fn.ExposedAs, fn.InferPermissions, fn.SessionArgument); err != nil {
			return fmt.Errorf("import tracked function %s: %w", fn.Function, err)
		}
	}
	for _, fp := range set.FunctionPermissions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO function_permissions (project_id, function_key, role_name) VALUES ($1, $2, $3)
			ON CONFLICT (project_id, function_key, role_name) DO NOTHING`,
			projectID, fp.Function, fp.Role); err != nil {
			return fmt.Errorf("import function permission %s/%s: %w", fp.Function, fp.Role, err)
		}
	}
	return nil
}

var _ storage.PermissionStore = (*PermissionStore)(nil)
