package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetRoles returns all non-system roles from pg_roles.
func (i *Introspector) GetRoles(ctx context.Context, db *sql.DB) ([]RoleInfo, error) {
	rows, err := db.QueryContext(ctx, rolesQuery)
	if err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}
	defer rows.Close()

	roles := make([]RoleInfo, 0)
	for rows.Next() {
		var r RoleInfo
		if err := rows.Scan(&r.Name, &r.Login, &r.Superuser, &r.CreateDB, &r.CreateRole, &r.ConnLimit); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// CreateRole creates a new PostgreSQL role. Cannot create roles with protected names.
func (i *Introspector) CreateRole(ctx context.Context, db *sql.DB, req CreateRoleRequest) error {
	if protectedRoles[strings.ToLower(req.Name)] {
		return fmt.Errorf("cannot create role with protected name %q", req.Name)
	}
	var stmt string
	if err := db.QueryRowContext(ctx, createRoleStatementQuery, req.Name, req.Login, req.Password).Scan(&stmt); err != nil {
		return fmt.Errorf("build create role: %w", err)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	return nil
}

// protectedRoles are system roles that cannot be dropped via the API.
var protectedRoles = map[string]bool{
	"postgres":          true,
	"excalibase_app":    true,
	"auth_admin":        true,
	"streaming_replica": true,
}

// DropRole drops a PostgreSQL role. Protected system roles cannot be dropped.
func (i *Introspector) DropRole(ctx context.Context, db *sql.DB, name string) error {
	lower := strings.ToLower(name)
	if protectedRoles[lower] {
		return fmt.Errorf("cannot drop protected role %q", name)
	}
	stmt := "DROP ROLE " + QuoteIdent(name)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}

// createRoleStatementQuery has Postgres quote the name and password itself,
// under the session's own string settings.
const createRoleStatementQuery = `
SELECT format('CREATE ROLE %I', $1::text)
    || CASE WHEN $2::boolean THEN ' LOGIN' ELSE '' END
    || CASE WHEN $3::text IS NULL THEN '' ELSE format(' PASSWORD %L', $3::text) END`

const rolesQuery = `
SELECT rolname, rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolconnlimit
FROM pg_roles
WHERE rolname NOT LIKE 'pg_%'
ORDER BY rolname`
