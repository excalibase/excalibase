package pgroles

import (
	"strings"
	"testing"
)

func TestRuntimeRoleIsReservedAndSquatGuarded(t *testing.T) {
	if !IsReserved(RuntimeRole) || !IsReserved("EXCALIBASE_RUNTIME") {
		t.Fatal("a customer may create the runtime role's name")
	}
	if !strings.Contains(SquatGuardSQL(), "'"+RuntimeRole+"'") {
		t.Fatal("the squat guard does not cover the runtime role")
	}
}

// EXC-765: the app's login is built from what a customer's code may do, so the
// role carries none of the attributes that create or alter another login.
func TestRuntimeRoleSQLHasNoRoleManagementAttributes(t *testing.T) {
	sql := RuntimeRoleSQL("shop_owner", "SCRAM-SHA-256$4096:verifier")
	for _, want := range []string{
		"NOSUPERUSER NOCREATEDB NOCREATEROLE", "NOREPLICATION NOBYPASSRLS",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, forbidden := range []string{" CREATEROLE", "WITH ADMIN", "ADMIN OPTION", " SUPERUSER", "GRANT EXECUTE ON FUNCTION excalibase.create_role"} {
		if strings.Contains(strings.ReplaceAll(sql, "NOCREATEROLE", ""), forbidden) {
			t.Errorf("the runtime SQL contains %q", forbidden)
		}
	}
}

// Postgres 16 can take SET ROLE away from a member; before it, the role
// functions' own session check is what keeps a member from acting as the owner.
func TestRuntimeRoleSQLInheritsWithoutSetRoleFromPostgres16(t *testing.T) {
	sql := RuntimeRoleSQL("shop_owner", "verifier")
	if !strings.Contains(sql, "WITH INHERIT TRUE, SET FALSE") || !strings.Contains(sql, "server_version_num") {
		t.Fatalf("membership is not granted without SET:\n%s", sql)
	}
}

func TestRuntimeRoleSQLCarriesNamesAndVerifierAsHexOnly(t *testing.T) {
	owner := `own"er'; DROP ROLE postgres; --`
	verifier := `SCRAM-SHA-256$x'; DROP ROLE postgres; --`
	sql := RuntimeRoleSQL(owner, verifier)
	if strings.Contains(sql, owner) || strings.Contains(sql, verifier) || strings.Contains(sql, "DROP ROLE postgres") {
		t.Fatalf("a name or verifier reached the SQL verbatim:\n%s", sql)
	}
}

func TestRuntimeLockSQLLeavesAnUnfiledLoginUnableToSignIn(t *testing.T) {
	sql := RuntimeLockSQL()
	if !strings.Contains(sql, "NOLOGIN") || !strings.Contains(sql, "to_regrole") {
		t.Fatalf("RuntimeLockSQL = %s", sql)
	}
}

// The role functions refuse any caller that did not sign in as the owner, so a
// role that inherits the owner's EXECUTE cannot mint a login.
func TestCustomerRoleFunctionsRefuseASessionThatIsNotTheOwner(t *testing.T) {
	sql := CustomerRoleSQL("shop_owner")
	if got := strings.Count(sql, "session_user <> owner_role"); got != 3 {
		t.Errorf("the owner-session check appears %d times, want once per function", got)
	}
}
