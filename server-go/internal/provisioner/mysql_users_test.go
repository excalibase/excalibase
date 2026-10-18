package provisioner

import (
	"strings"
	"testing"
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
		"GRANT SELECT ON `auth`.* TO 'excalibase_app'@'%';",
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
