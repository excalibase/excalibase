//go:build integration

package handler

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// EXC-628: the database URL a function is handed is the project owner's login,
// and that login cannot read the project's end-user auth tables, which only
// auth_admin reaches.
func TestFunctionDBURL_CannotReadTheAuthTables(t *testing.T) {
	db, logins := startProjectPG(t)
	mustExecE2E(t, db, `CREATE ROLE tenant_owner LOGIN PASSWORD 'owner-pass'`)
	mustExecE2E(t, db, `ALTER DATABASE tenant OWNER TO tenant_owner`)
	mustExecE2E(t, db, pgroles.CustomerRoleSQL("tenant_owner")+pgroles.OwnerAccessSQL("tenant_owner"))
	authAdmin := openWiringDB(t, logins["auth_admin"])
	mustExecE2E(t, authAdmin, `CREATE TABLE auth.users (id serial PRIMARY KEY, password text);
		INSERT INTO auth.users (password) VALUES ('$2a$10$hash')`)

	app := logins["excalibase_app"]
	v := newFakeVault()
	v.data["projects/"+wiringProjectID+"/credentials/admin"] = map[string]string{
		"host": app["host"], "port": app["port"], "database": "tenant", "username": "tenant_owner", "password": "owner-pass",
	}
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), edgefn.NewSecretsStore(v), nil,
		&inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{wiringProjectID: {ProjectID: wiringProjectID, OrgID: "default"}}},
		nil, testAPIBase)
	h.SetVault(v)
	url := h.builtinEnv(context.Background(), wiringProjectID)["EXCALIBASE_DB_URL"]
	if !strings.Contains(url, "tenant_owner:") {
		t.Fatalf("the function's database URL is not the owner login: %q", url)
	}

	// The test database serves no TLS; the login is what is under test.
	function, err := sql.Open("postgres", strings.Replace(url, "sslmode=require", "sslmode=disable", 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = function.Close() })
	mustExecE2E(t, function, `CREATE TABLE public.notes (id int)`)
	for _, statement := range []string{
		`SELECT password FROM auth.users`,
		`INSERT INTO auth.users (password) VALUES ('planted')`,
		`GRANT SELECT ON auth.users TO tenant_owner`,
	} {
		if _, err := function.Exec(statement); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("the function's login ran %s: %v", statement, err)
		}
	}
}
