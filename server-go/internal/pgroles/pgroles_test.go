package pgroles

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

func TestEveryPlatformRoleIsReserved(t *testing.T) {
	names := append([]string{
		"postgres", "streaming_replica",
		config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup, config.DocumentDBBrowserLogin,
	}, tenantcert.PlatformRoles...)
	for _, name := range names {
		if !IsReserved(name) {
			t.Errorf("%s is not reserved", name)
		}
		if !IsReserved(strings.ToUpper(name)) {
			t.Errorf("%s in upper case is not reserved", name)
		}
	}
}

func TestReservedPrefixes(t *testing.T) {
	for _, name := range []string{
		"pg_execute_server_program", "pg_anything", "documentdb_admin_role", "documentdb_bg_worker_role",
		"excalibase_future", "cnpg_pooler_pgbouncer", "Excalibase_App",
	} {
		if !IsReserved(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
}

func TestCustomerNamesAreNotReserved(t *testing.T) {
	for _, name := range []string{"analytics", "reporting_ro", "anon", "authenticated", "app_reader", "Mixed Case", `a"b`} {
		if IsReserved(name) {
			t.Errorf("%s is reserved", name)
		}
	}
}

func TestReservedListsAreCopies(t *testing.T) {
	names := ReservedNames()
	names[0] = "changed"
	if ReservedNames()[0] == "changed" {
		t.Fatal("ReservedNames hands out the shared slice")
	}
	prefixes := ReservedPrefixes()
	prefixes[0] = "changed"
	if ReservedPrefixes()[0] == "changed" {
		t.Fatal("ReservedPrefixes hands out the shared slice")
	}
}

// The names are rendered into the functions as plain quoted literals.
func TestReservedNamesArePlainIdentifiers(t *testing.T) {
	plain := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, name := range append(ReservedNames(), ReservedPrefixes()...) {
		if !plain.MatchString(name) {
			t.Errorf("%q is not a plain lowercase identifier", name)
		}
	}
	if !plain.MatchString(config.DocumentDBMongoUsersGroup) || !slices.Contains(tenantcert.PlatformRoles, appRole) {
		t.Fatal("the Mongo group or the app role is not what the functions expect")
	}
}
