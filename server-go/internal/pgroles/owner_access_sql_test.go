package pgroles

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestOwnerAccessSQLReplacesWhatThePlatformRoleCouldHavePlanted(t *testing.T) {
	sql := OwnerAccessSQL("app")
	for _, want := range []string{
		"DROP EVENT TRIGGER IF EXISTS " + ownerAccessTrigger + ";",
		"DROP FUNCTION IF EXISTS " + ShareWithOwnerSignature + ";",
		"DROP FUNCTION IF EXISTS " + shareCreatedSchemaSignature + ";",
		"ALTER FUNCTION " + ShareWithOwnerSignature + " OWNER TO CURRENT_USER",
		"ALTER FUNCTION " + shareCreatedSchemaSignature + " OWNER TO CURRENT_USER",
		"REVOKE ALL ON FUNCTION " + ShareWithOwnerSignature + " FROM PUBLIC",
		"CREATE EVENT TRIGGER " + ownerAccessTrigger + " ON ddl_command_end WHEN TAG IN ('CREATE SCHEMA')",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// Both functions run with the caller's rights: the trigger as excalibase_app,
// the registration call as the superuser. Neither may carry DEFINER rights a
// customer could borrow.
func TestOwnerAccessSQLFunctionsAreInvokerWithPinnedSearchPath(t *testing.T) {
	sql := OwnerAccessSQL("app")
	if strings.Contains(sql, "SECURITY DEFINER") {
		t.Fatal("an owner-access function runs with its definer's rights")
	}
	if got := strings.Count(sql, "SET search_path = pg_catalog, pg_temp"); got != 2 {
		t.Errorf("pinned search_path appears %d times, want 2", got)
	}
}

// Only schemas the platform role created, or public, are shared; the
// reserved platform schemas never are.
func TestOwnerAccessSQLSharesOnlyUserSchemasThePlatformOwns(t *testing.T) {
	sql := OwnerAccessSQL("app")
	for _, want := range []string{
		UserSchemaPredicate,
		"n.nspname = 'public' OR n.nspowner = to_regrole(platform_role)",
		"c.relowner = to_regrole(platform_role)",
		"IF current_user <> platform_role THEN",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, schema := range []string{"'auth'", "'excalibase'", "'pg_'", "'excalibase_'"} {
		if !strings.Contains(UserSchemaPredicate, schema) {
			t.Errorf("user schema predicate does not exclude %s", schema)
		}
	}
}

func TestOwnerAccessSQLCarriesTheOwnerAsHexOnly(t *testing.T) {
	owner := `own"er'; DROP ROLE postgres; --`
	sql := OwnerAccessSQL(owner)
	if strings.Contains(sql, owner) || strings.Contains(sql, "DROP ROLE postgres") {
		t.Fatalf("the owner name reached the SQL text verbatim:\n%s", sql)
	}
	if !strings.Contains(sql, hex.EncodeToString([]byte(owner))) {
		t.Fatal("the owner name is not in the SQL at all")
	}
}

// Registration heals what a restored or older cluster already holds.
func TestOwnerAccessSQLSharesExistingSchemasAtRegistration(t *testing.T) {
	sql := OwnerAccessSQL("app")
	if !strings.Contains(sql, "PERFORM excalibase.share_with_owner(n.oid) FROM pg_namespace n") {
		t.Fatalf("registration does not share existing schemas:\n%s", sql)
	}
}
