package pgroles

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCustomerRoleSQLDefinesThreeLockedDownFunctions(t *testing.T) {
	sql := CustomerRoleSQL("app")
	for _, signature := range []string{CreateRoleSignature, AlterRolePasswordSignature, DropRoleSignature} {
		for _, want := range []string{
			"DROP FUNCTION IF EXISTS " + signature + ";\nCREATE FUNCTION " + strings.SplitN(signature, "(", 2)[0] + "(role_name text",
			"ALTER FUNCTION " + signature + " OWNER TO CURRENT_USER",
			"REVOKE ALL ON FUNCTION " + signature + " FROM PUBLIC",
			"GRANT EXECUTE ON FUNCTION " + signature + " TO %I, %I",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("missing %q", want)
			}
		}
	}
	if got := strings.Count(sql, "SECURITY DEFINER"); got != 3 {
		t.Errorf("SECURITY DEFINER appears %d times, want 3", got)
	}
	if got := strings.Count(sql, "SET search_path = pg_catalog, pg_temp"); got != 3 {
		t.Errorf("pinned search_path appears %d times, want 3", got)
	}
}

func TestCustomerRoleSQLGivesCreateRoleOnlyFromPostgres16(t *testing.T) {
	sql := CustomerRoleSQL("app")
	if !strings.Contains(sql, "current_setting('server_version_num')::int >= 160000 THEN EXECUTE format('ALTER ROLE %I CREATEROLE'") {
		t.Fatalf("CREATEROLE is not gated on Postgres 16:\n%s", sql)
	}
}

func TestCustomerRoleSQLEmbedsTheSharedReservedList(t *testing.T) {
	sql := CustomerRoleSQL("app")
	for _, name := range ReservedNames() {
		if !strings.Contains(sql, "'"+name+"'") {
			t.Errorf("reserved name %s is not enforced by the functions", name)
		}
	}
	for _, prefix := range ReservedPrefixes() {
		if !strings.Contains(sql, "'"+prefix+"'") {
			t.Errorf("reserved prefix %s is not enforced by the functions", prefix)
		}
	}
}

func TestCustomerRoleSQLCarriesTheOwnerAsHexOnly(t *testing.T) {
	owner := `own"er'; DROP ROLE postgres; --`
	sql := CustomerRoleSQL(owner)
	if strings.Contains(sql, owner) || strings.Contains(sql, "DROP ROLE postgres") {
		t.Fatalf("the owner name reached the SQL text verbatim:\n%s", sql)
	}
	if !strings.Contains(sql, hex.EncodeToString([]byte(owner))) {
		t.Fatal("the owner name is not in the SQL at all")
	}
}

func TestCustomerRoleSQLNeverCallsOutOfPgCatalog(t *testing.T) {
	sql := CustomerRoleSQL("app")
	// excalibase_app may create objects in the excalibase schema; a body that
	// called one would run it as the superuser.
	body := sql[strings.Index(sql, "CREATE FUNCTION"):]
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "excalibase.") && !strings.HasPrefix(trimmed, "CREATE FUNCTION") && !strings.HasPrefix(trimmed, "DROP FUNCTION") &&
			!strings.HasPrefix(trimmed, "ALTER FUNCTION") && !strings.HasPrefix(trimmed, "REVOKE ALL ON FUNCTION") &&
			!strings.HasPrefix(trimmed, "EXECUTE format('GRANT EXECUTE ON FUNCTION") {
			t.Errorf("a function body refers to the excalibase schema: %s", trimmed)
		}
	}
}

// A superuser script resolves unqualified names through search_path; a
// customer's function in public must never be what it calls.
func TestSuperuserScriptPinsSearchPath(t *testing.T) {
	if !strings.HasPrefix(PinnedSearchPath, "SET search_path = pg_catalog, pg_temp;") {
		t.Fatalf("PinnedSearchPath is %q", PinnedSearchPath)
	}
}

func TestSquatGuardCoversEveryReservedName(t *testing.T) {
	sql := SquatGuardSQL()
	for _, name := range ReservedNames() {
		if !strings.Contains(sql, "'"+name+"'") {
			t.Errorf("%s is not checked", name)
		}
	}
	if !strings.Contains(sql, "admin_option") || !strings.Contains(sql, "NOT u.rolsuper") {
		t.Fatalf("the guard does not look for a non-superuser administering a platform role:\n%s", sql)
	}
}
