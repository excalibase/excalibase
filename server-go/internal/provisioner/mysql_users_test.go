package provisioner

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ADR 0041 decision 5: the platform's own users, never MOCO's, each with
// only what it needs and every one over TLS.
func TestMySQLPlatformUsersSQL(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{
		Admin: "a1", App: "b2", AuthAdmin: "c3", Watcher: "d4",
	})
	for _, want := range []string{
		"CREATE DATABASE IF NOT EXISTS `auth`;",
		"CREATE DATABASE IF NOT EXISTS `excalibase`;",
		"CREATE USER IF NOT EXISTS 'admin'@'%' IDENTIFIED BY 'a1' REQUIRE SSL;",
		"ALTER USER 'admin'@'%' IDENTIFIED BY 'a1' REQUIRE SSL;",
		"GRANT ALL PRIVILEGES ON `app`.* TO 'admin'@'%';",
		"GRANT ALL PRIVILEGES ON `app`.* TO 'excalibase_app'@'%';",
		"GRANT ALL PRIVILEGES ON `excalibase`.* TO 'excalibase_app'@'%';",
		// EXC-628: a restored cluster may carry the old read grant.
		MySQLAppAuthRevokeSQL,
		"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES ON `auth`.* TO 'auth_admin'@'%';",
		"GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'cdc_watcher'@'%';",
		"GRANT SELECT ON `app`.* TO 'cdc_watcher'@'%';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(sql, "REQUIRE SSL") != 8 {
		t.Errorf("every user is created and altered with REQUIRE SSL:\n%s", sql)
	}
	if strings.Contains(sql, "`auth`.* TO 'excalibase_app'") || strings.Contains(sql, "`auth`.* TO 'admin'") {
		t.Error("only auth_admin is granted anything in the auth database")
	}
	if strings.Contains(sql, "*.* TO 'admin'") || strings.Contains(sql, "*.* TO 'excalibase_app'") {
		t.Error("only the watcher holds a global privilege")
	}
	// auth_admin works only in auth, with what the auth service's migrations
	// and queries use (EXC-626): no routines, events, triggers, views or locks.
	for _, line := range strings.Split(sql, "\n") {
		if strings.Contains(line, "TO 'auth_admin'") && strings.HasPrefix(line, "GRANT") &&
			line != "GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES ON `auth`.* TO 'auth_admin'@'%';" {
			t.Errorf("auth_admin is granted more: %s", line)
		}
	}
}

func TestMySQLPlatformUsersSQL_QuotesPasswords(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{Admin: `it's\`, App: "b", AuthAdmin: "c", Watcher: "d"})
	if !strings.Contains(sql, `IDENTIFIED BY 'it''s\\'`) {
		t.Errorf("password not escaped:\n%s", sql)
	}
}

func TestMySQLAlterUserSQL(t *testing.T) {
	if got := MySQLAlterUserSQL("excalibase_app", "n3w'"); got != "ALTER USER 'excalibase_app'@'%' IDENTIFIED BY 'n3w''' REQUIRE SSL;" {
		t.Errorf("got %s", got)
	}
}

// EXC-765: the logins customer code can hold (the owner's and the runtime
// login) are granted the project database and nothing global, so none can
// create a user, grant a privilege onward or change the server.
func TestMySQLCustomerFacingUsersHoldNoGlobalPrivilege(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{
		Admin: "a1", App: "b2", AuthAdmin: "c3", Watcher: "d4", Reader: "e5", Runtime: "f6",
	})
	for _, want := range []string{
		"CREATE USER IF NOT EXISTS 'excalibase_runtime'@'%' IDENTIFIED BY 'f6' REQUIRE SSL;",
		"ALTER USER 'excalibase_runtime'@'%' ACCOUNT UNLOCK;",
		"GRANT ALL PRIVILEGES ON `app`.* TO 'excalibase_runtime'@'%';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, line := range customerFacingGrants(sql) {
		for _, forbidden := range []string{"GRANT OPTION", "CREATE USER", "SUPER", "*.*", "ROLE_ADMIN", "SYSTEM_VARIABLES_ADMIN"} {
			if strings.Contains(line, forbidden) {
				t.Errorf("a login customer code can hold has %s: %s", forbidden, line)
			}
		}
	}
}

// customerFacingGrants are the GRANT lines of the owner's, the platform app's
// and the runtime login.
func customerFacingGrants(sql string) []string {
	var grants []string
	for _, line := range strings.Split(sql, "\n") {
		for _, user := range []string{"'admin'", "'excalibase_app'", "'excalibase_runtime'"} {
			if strings.HasPrefix(line, "GRANT") && strings.Contains(line, "TO "+user) {
				grants = append(grants, line)
			}
		}
	}
	return grants
}

func TestMySQLRuntimeLoginIsLockedWithoutAPassword(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{Admin: "a1", App: "b2", AuthAdmin: "c3", Watcher: "d4"})
	if !strings.Contains(sql, "ALTER USER IF EXISTS 'excalibase_runtime'@'%' ACCOUNT LOCK;") {
		t.Error("a runtime login a restored copy brought is left open")
	}
	if strings.Contains(sql, "CREATE USER IF NOT EXISTS 'excalibase_runtime'") {
		t.Error("a runtime login was created without a password")
	}
}

// EXC-765 review: a database grant names a pattern, where _ and % match other
// names, so "aut_" would reach "auth". The name is escaped in every grant, and
// grants an earlier run made with the pattern are revoked first.
func TestMySQLGrantsEscapeWildcardsInTheDatabaseName(t *testing.T) {
	sql := MySQLPlatformUsersSQL("aut_", MySQLPasswords{Admin: "a", App: "b", AuthAdmin: "c", Watcher: "d", Reader: "e", Runtime: "f"})
	if !strings.Contains(sql, "CREATE DATABASE IF NOT EXISTS `auth`;") {
		t.Error("the auth database is created under its own name")
	}
	for _, user := range []string{"admin", "excalibase_app", "cdc_watcher", "excalibase_reader", "excalibase_runtime"} {
		revoke := "REVOKE IF EXISTS ALL PRIVILEGES ON `aut_`.* FROM '" + user + "'@'%' IGNORE UNKNOWN USER;"
		if !strings.Contains(sql, revoke) {
			t.Errorf("missing %s", revoke)
		}
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(line, "GRANT") && strings.Contains(line, "`aut_`") {
			t.Errorf("a grant names the database as a pattern: %s", line)
		}
	}
	for _, want := range []string{
		"GRANT ALL PRIVILEGES ON `aut\\_`.* TO 'admin'@'%';",
		"GRANT ALL PRIVILEGES ON `aut\\_`.* TO 'excalibase_runtime'@'%';",
		"GRANT SELECT ON `aut\\_`.* TO 'cdc_watcher'@'%';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s", want)
		}
	}
	// The revokes come before the grants, or they would take the new ones back.
	if strings.Index(sql, "REVOKE IF EXISTS ALL PRIVILEGES ON `aut_`") > strings.Index(sql, "GRANT ALL PRIVILEGES ON `aut\\_`") {
		t.Error("the pattern revokes run after the escaped grants")
	}
	if plain := MySQLPlatformUsersSQL("shop", MySQLPasswords{Admin: "a", App: "b", AuthAdmin: "c", Watcher: "d"}); strings.Contains(plain, "ON `shop`.* FROM") {
		t.Error("a name with no wildcard needs no revoke")
	}
}

func TestMySQLReservedDatabaseNamesAreRefused(t *testing.T) {
	for _, name := range []string{"auth", "excalibase", "mysql", "sys", "information_schema", "performance_schema", "excalibase_x"} {
		if _, _, err := validateMySQLRequest(domain.ProvisioningRequest{DatabaseName: name}); err == nil {
			t.Errorf("%q was accepted as a project database", name)
		}
	}
	if _, _, err := validateMySQLRequest(domain.ProvisioningRequest{DatabaseName: "aut_"}); err != nil {
		t.Errorf("a name with an underscore is fine once escaped: %v", err)
	}
}
