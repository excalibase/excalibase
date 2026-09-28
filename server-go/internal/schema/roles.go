package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
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

// ErrProtectedRole refuses a role the platform holds or reserves.
var ErrProtectedRole = errors.New("this role belongs to the platform")

// CreateRole creates one of the project's own roles through
// excalibase.create_role: Studio runs as excalibase_app, which has
// no CREATEROLE on any major, and the function makes the role the owner's.
// The password travels as its SCRAM verifier, a bind parameter, so the
// plaintext never reaches the database.
func (i *Introspector) CreateRole(ctx context.Context, db *sql.DB, req CreateRoleRequest) error {
	if pgroles.IsReserved(req.Name) {
		return fmt.Errorf("cannot create role %q: %w", req.Name, ErrProtectedRole)
	}
	var verifier *string
	if req.Password != nil {
		hashed, err := pgscram.Verifier(*req.Password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		verifier = &hashed
	}
	if _, err := db.ExecContext(ctx, "SELECT excalibase.create_role($1, $2, $3)", req.Name, verifier, req.Login); err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	return nil
}

// DropRole drops one of the project's own roles through excalibase.drop_role,
// which refuses any role the owner did not create, Mongo users included.
func (i *Introspector) DropRole(ctx context.Context, db *sql.DB, name string) error {
	if pgroles.IsReserved(name) {
		return fmt.Errorf("cannot drop role %q: %w", name, ErrProtectedRole)
	}
	if _, err := db.ExecContext(ctx, "SELECT excalibase.drop_role($1)", name); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}

const rolesQuery = `
SELECT rolname, rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolconnlimit
FROM pg_roles
WHERE rolname NOT LIKE 'pg_%'
ORDER BY rolname`
