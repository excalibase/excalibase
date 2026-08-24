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
	stmt := "CREATE ROLE " + QuoteIdent(req.Name)
	if req.Login {
		stmt += " LOGIN"
	}
	if req.Password != nil {
		stmt += " PASSWORD " + QuoteLiteral(*req.Password)
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

const rolesQuery = `
SELECT rolname, rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolconnlimit
FROM pg_roles
WHERE rolname NOT LIKE 'pg_%'
ORDER BY rolname`
