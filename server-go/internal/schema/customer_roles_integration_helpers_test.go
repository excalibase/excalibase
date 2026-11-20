//go:build integration

package schema

import (
	"context"
	"database/sql"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// testOwner stands in for the cluster owner CloudNativePG creates.
const testOwner = "app_owner"

// installCustomerRoleFunctions puts in what provisioning installs: the
// functions Studio creates and drops customer roles through.
func installCustomerRoleFunctions(t *testing.T, superDB *sql.DB) {
	t.Helper()
	setup := `
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'excalibase_app') THEN CREATE ROLE excalibase_app LOGIN PASSWORD 'apppass'; END IF;
END $$;
CREATE ROLE ` + testOwner + ` LOGIN PASSWORD 'ownerpass';
CREATE SCHEMA IF NOT EXISTS excalibase;
GRANT USAGE ON SCHEMA excalibase TO excalibase_app;
`
	if _, err := superDB.ExecContext(context.Background(), setup+pgroles.CustomerRoleSQL(testOwner)); err != nil {
		t.Fatalf("install customer role functions: %v", err)
	}
}

// connectAsOwner opens the project owner's login, the one that runs role
// management: the app role, which runs customer SQL, may not (EXC-761).
func connectAsOwner(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", testPGHostPort+" user="+testOwner+" password=ownerpass dbname=testdb sslmode=disable")
	if err != nil {
		t.Fatalf("connect as the owner: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
