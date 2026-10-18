package provisioner

import (
	"fmt"
	"strings"
)

// The platform's MySQL users (ADR 0041 decision 5). MOCO's own moco-* users
// stay the operator's.
const (
	MySQLUserAdmin     = "admin"
	MySQLUserApp       = "excalibase_app"
	MySQLUserAuthAdmin = "auth_admin"
	MySQLUserWatcher   = "cdc_watcher"
	// MySQLAuthDatabase and MySQLPlatformDatabase stand in for Postgres's
	// auth and excalibase schemas: MySQL has no schemas inside a database.
	MySQLAuthDatabase     = "auth"
	MySQLPlatformDatabase = "excalibase"
)

// MySQLAuthAdminPrivileges is what excalibase-auth's migrations and queries
// use in the auth database, and nothing more (EXC-626).
const MySQLAuthAdminPrivileges = "SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES"

// MySQLPasswords are the passwords the platform users are created with.
type MySQLPasswords struct {
	Admin, App, AuthAdmin, Watcher string
}

// MySQLPlatformUsersSQL creates the auth and platform databases and the
// platform users with their grants. Users that exist (a restored cluster) get
// the given passwords, so vault stays the record of what opens the database.
func MySQLPlatformUsersSQL(projectDatabase string, passwords MySQLPasswords) string {
	project, auth, platform := mysqlIdent(projectDatabase), mysqlIdent(MySQLAuthDatabase), mysqlIdent(MySQLPlatformDatabase)
	var sql strings.Builder
	fmt.Fprintf(&sql, "CREATE DATABASE IF NOT EXISTS %s;\nCREATE DATABASE IF NOT EXISTS %s;\n", auth, platform)
	for _, user := range []struct{ name, password string }{
		{MySQLUserAdmin, passwords.Admin}, {MySQLUserApp, passwords.App},
		{MySQLUserAuthAdmin, passwords.AuthAdmin}, {MySQLUserWatcher, passwords.Watcher},
	} {
		fmt.Fprintf(&sql, "CREATE USER IF NOT EXISTS %s IDENTIFIED BY %s REQUIRE SSL;\n%s\n",
			mysqlAccount(user.name), mysqlString(user.password), MySQLAlterUserSQL(user.name, user.password))
	}
	for _, grant := range []string{
		"ALL PRIVILEGES ON " + project + ".* TO " + mysqlAccount(MySQLUserAdmin),
		"ALL PRIVILEGES ON " + project + ".* TO " + mysqlAccount(MySQLUserApp),
		"ALL PRIVILEGES ON " + platform + ".* TO " + mysqlAccount(MySQLUserApp),
		"SELECT ON " + auth + ".* TO " + mysqlAccount(MySQLUserApp),
		MySQLAuthAdminPrivileges + " ON " + auth + ".* TO " + mysqlAccount(MySQLUserAuthAdmin),
		"REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO " + mysqlAccount(MySQLUserWatcher),
		"SELECT ON " + project + ".* TO " + mysqlAccount(MySQLUserWatcher),
	} {
		fmt.Fprintf(&sql, "GRANT %s;\n", grant)
	}
	return sql.String()
}

// MySQLAlterUserSQL sets a platform user's password, TLS still required.
func MySQLAlterUserSQL(user, password string) string {
	return fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s REQUIRE SSL;", mysqlAccount(user), mysqlString(password))
}

func mysqlAccount(user string) string { return mysqlString(user) + "@'%'" }

// mysqlString quotes a string literal for MySQL's default sql_mode, where a
// backslash escapes.
func mysqlString(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value)
	return "'" + escaped + "'"
}

func mysqlIdent(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
