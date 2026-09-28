package service

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// TestBuildProjectRoleSQL_CreatesCdcWatcherRoleWithReplication asserts the
// new dedicated CDC role is created with the REPLICATION attribute, separate
// from excalibase_app. This is the security boundary — only cdc_watcher can
// open replication connections; excalibase_app cannot dump WAL.
func TestBuildProjectRoleSQL_CreatesCdcWatcherRoleWithReplication(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	if !strings.Contains(sql, "rolname = 'cdc_watcher'") {
		t.Error("expected cdc_watcher role creation guard")
	}
	if !strings.Contains(strings.ToUpper(sql), "REPLICATION") {
		t.Error("expected REPLICATION attribute on cdc_watcher")
	}
}

// TestBuildProjectRoleSQL_DoesNotAlterRoleApp asserts we no longer rely on
// CNPG's default `app` role for replication. The legacy ALTER ROLE app WITH
// REPLICATION is removed in favor of a dedicated cdc_watcher role.
func TestBuildProjectRoleSQL_DoesNotAlterRoleApp(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	if strings.Contains(sql, "ALTER ROLE app WITH REPLICATION") {
		t.Error("ALTER ROLE app WITH REPLICATION should be removed; use cdc_watcher instead")
	}
}

// TestBuildProjectRoleSQL_PublicationIsEmpty asserts the publication is
// created without FOR ALL TABLES. Opt-in by default — users (or the
// NoSQL auto-create flow) explicitly add tables.
func TestBuildProjectRoleSQL_PublicationIsEmpty(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	if !strings.Contains(sql, "format('CREATE PUBLICATION %I', "+sqlTextLiteral("cdc_watcher_pub")+")") {
		t.Errorf("expected CREATE PUBLICATION of cdc_watcher_pub; sql:\n%s", sql)
	}
	if strings.Contains(sql, "FOR ALL TABLES") {
		t.Error("publication must be empty (no FOR ALL TABLES) — opt-in only")
	}
}

// TestBuildProjectRoleSQL_HonoursCustomPublicationName asserts the
// publication name parameter is threaded through — both the existence
// check and the CREATE statement use the supplied name. Critical
// because the watcher daemon's config is the source of truth; tests
// and e2e setups must be able to use non-default names.
func TestBuildProjectRoleSQL_HonoursCustomPublicationName(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "custom_pub_name")

	name := sqlTextLiteral("custom_pub_name")
	if !strings.Contains(sql, "pubname = "+name) {
		t.Error("expected pubname existence check against custom_pub_name")
	}
	if !strings.Contains(sql, "format('CREATE PUBLICATION %I', "+name+")") {
		t.Error("expected CREATE PUBLICATION of custom_pub_name")
	}
	if !strings.Contains(sql, "target_publication CONSTANT text := "+name) {
		t.Error("expected the realtime function to be bound to custom_pub_name")
	}
}

// TestBuildProjectRoleSQL_PublicationChangesOnlyThroughTheRealtimeFunction
// asserts excalibase_app changes membership through the function only: ALTER
// PUBLICATION ADD TABLE needs ownership of the table, which excalibase_app
// does not have for tables the project owner role created.
func TestBuildProjectRoleSQL_PublicationChangesOnlyThroughTheRealtimeFunction(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	if strings.Contains(sql, `OWNER TO "excalibase_app"`) {
		t.Errorf("excalibase_app must not own the publication; sql:\n%s", sql)
	}
	for _, want := range []string{
		`ALTER PUBLICATION "cdc_watcher_pub" OWNER TO CURRENT_USER`,
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, pg_temp",
		"REVOKE ALL ON FUNCTION excalibase.set_realtime_table(text, text, boolean) FROM PUBLIC",
		`GRANT EXECUTE ON FUNCTION excalibase.set_realtime_table(text, text, boolean) TO "excalibase_app"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected %q; sql:\n%s", want, sql)
		}
	}
}

// TestBuildProjectRoleSQL_ExcalibaseAppHasNoReplicationAttribute asserts
// excalibase_app is NOT granted REPLICATION. With REPLICATION, a leaked
// excalibase_app credential would let an attacker dump raw WAL bypassing
// RLS — including auth tables. Keeping it without REPLICATION is the
// load-bearing security boundary.
func TestBuildProjectRoleSQL_ExcalibaseAppHasNoReplicationAttribute(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	// The role creation block for excalibase_app must not include REPLICATION
	excalibaseAppBlock := extractRoleBlock(sql, "excalibase_app")
	if strings.Contains(strings.ToUpper(excalibaseAppBlock), "REPLICATION") {
		t.Errorf("excalibase_app must NOT have REPLICATION attribute; block:\n%s", excalibaseAppBlock)
	}
	if strings.Contains(sql, `ALTER ROLE "excalibase_app" WITH REPLICATION`) ||
		strings.Contains(sql, "ALTER ROLE excalibase_app WITH REPLICATION") {
		t.Error("excalibase_app must NOT be granted REPLICATION via ALTER ROLE")
	}
}

// extractRoleBlock returns the DO block that creates the named role, so
// tests can assert on attributes within the relevant region.
func extractRoleBlock(sql, role string) string {
	marker := "rolname = '" + role + "'"
	start := strings.Index(sql, marker)
	if start < 0 {
		return ""
	}
	end := strings.Index(sql[start:], "END $$")
	if end < 0 {
		return sql[start:]
	}
	return sql[start : start+end]
}

func TestBuildProjectRoleSQL_NeverEmbedsSecretText(t *testing.T) {
	hostile := []string{"a$$; SELECT 1; DO $$ BEGIN", `x\'; --`, "$t$'\"", "pw'd"}
	for _, pw := range hostile {
		sql := BuildProjectRoleSQL(pw, pw, pw, "app", "cdc_watcher_pub") +
			BuildProjectRoleResetSQL([]RolePassword{{Role: "owner$$", Password: pw}})
		if strings.Contains(sql, pw) {
			t.Errorf("password %q appears verbatim in the SQL", pw)
		}
		if strings.Contains(sql, "owner$$") {
			t.Error("role name appears verbatim inside a dollar-quoted body")
		}
	}
}

func TestSQLTextLiteral_UsesOnlyHexDigits(t *testing.T) {
	got := sqlTextLiteral("$$'\\")
	want := "convert_from(decode('2424275c', 'hex'), 'UTF8')"
	if got != want {
		t.Errorf("sqlTextLiteral = %s, want %s", got, want)
	}
}

func TestBuildProjectRoleSQL_WatcherWritesOnlyItsOwnSchema(t *testing.T) {
	sql := BuildProjectRoleSQL("authPass", "appPass", "watcherPass", "app", "cdc_watcher_pub")

	for _, want := range []string{
		`GRANT USAGE, CREATE ON SCHEMA excalibase_cdc TO "cdc_watcher"`,
		`ALTER ROLE "cdc_watcher" IN DATABASE "app" SET search_path = excalibase_cdc`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected %q; sql:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `SCHEMA public TO "cdc_watcher"`) {
		t.Error("cdc_watcher must not be granted anything on public")
	}
}

// Every registration, restore included, installs the customer role
// functions for the project's owner, before any password reset.
func TestProjectRoleSQL_InstallsTheCustomerRoleFunctionsForTheOwner(t *testing.T) {
	spec := projectRoleSpec{databaseName: "app", adminUsername: "owner_x", resetPasswords: true}
	creds := projectRoleCredentials{authPassword: "a", appPassword: "b", watcherPassword: "c"}
	sql := projectRoleSQL(spec, creds, "pub")
	functions := strings.Index(sql, pgroles.CustomerRoleSQL("owner_x"))
	if functions < 0 {
		t.Fatal("the customer role functions are not installed")
	}
	if reset := strings.Index(sql, "ALTER ROLE %I WITH LOGIN PASSWORD"); reset < functions {
		t.Fatalf("the password reset runs before the functions are installed (%d < %d)", reset, functions)
	}
}
