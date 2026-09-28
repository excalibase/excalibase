//go:build integration

package service

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
	"github.com/lib/pq"
)

// A project's owner creates its own Postgres roles. On 16 and later
// it holds CREATEROLE; on 15 and earlier, where CREATEROLE lets a role grant
// itself pg_execute_server_program, it goes through excalibase.create_role.
// Either way it can reach nothing the platform or its Mongo users hold.

const (
	customerOwner         = "owner_app"
	customerOwnerPassword = "Owner-Password-1"
	customerAppPassword   = "App-Password-1"
	mongoWriter           = "mongo_writer"
)

func TestCustomerRolesOnPostgres15(t *testing.T) { customerRoleSuite(t, "postgres:15-alpine", false) }

func TestCustomerRolesOnPostgres17(t *testing.T) { customerRoleSuite(t, "postgres:17-alpine", true) }

// protectedRoles are what a project database holds that is not the owner's:
// the platform's roles, the DocumentDB roles and a Mongo user.
func protectedRoles() []string {
	return []string{
		roleApp, roleAuthAdmin, roleWatcher, "postgres", "streaming_replica",
		config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup, config.DocumentDBBrowserLogin,
		"documentdb_admin_role", mongoWriter, "other_role", customerOwner,
	}
}

// customerRoleCase is one cluster the scenarios run against, in order.
type customerRoleCase struct {
	pg         *rolePostgres
	owner      *sql.DB
	admin      *sql.DB
	spec       projectRoleSpec
	creds      projectRoleCredentials
	createRole bool
}

func customerRoleSuite(t *testing.T, image string, createRole bool) {
	pg := startRolePostgresImage(t, image)
	pg.psql(t, customerClusterSetupSQL())
	c := &customerRoleCase{
		pg:         pg,
		spec:       projectRoleSpec{databaseName: "app", adminUsername: customerOwner},
		creds:      projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: customerAppPassword, watcherPassword: "Watcher-Password-1"},
		createRole: createRole,
	}
	pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.owner = pg.connect(t, customerOwner, customerOwnerPassword)
	c.admin = pg.connect(t, "postgres", "p")

	t.Run("owner attribute", c.ownerAttribute)
	t.Run("function creates a login role the owner can grant to", c.functionCreatesALoginRole)
	t.Run("function refuses reserved and taken names", c.functionRefusesReservedNames)
	t.Run("function grants only roles the owner created", c.functionGrantsOnlyOwnedRoles)
	t.Run("function re-passwords and drops only roles the owner created", c.functionManagesOnlyOwnedRoles)
	t.Run("names are quoted, passwords are data", c.namesAreQuoted)
	t.Run("a pre-hashed password is stored as given", c.preHashedPassword)
	t.Run("only the owner and the platform app role may call the functions", c.onlyOwnerAndAppMayCall)
	if createRole {
		t.Run("owner creates, re-passwords and drops its own roles in SQL", c.ownerManagesRolesInSQL)
		t.Run("owner cannot grant any predefined role", c.ownerCannotGrantPredefinedRoles)
		t.Run("owner cannot touch a role it did not create", c.ownerCannotTouchOtherRoles)
		t.Run("owner cannot create privileged roles", c.ownerCannotCreatePrivilegedRoles)
		t.Run("owner drops what it created", c.ownerDropsItsRole)
		t.Run("registration refuses a platform role name the owner took", c.registrationRefusesASquattedRole)
	} else {
		t.Run("owner has no CREATEROLE to misuse", c.ownerHasNoCreateRole)
	}
	t.Run("owner revokes a membership the function granted", c.ownerRevokesFunctionGrant)
	t.Run("Mongo user cleanup and re-registration leave customer roles", c.restoreLogicKeepsCustomerRoles)
	t.Run("registration never calls a function planted in public", c.registrationIgnoresPlantedFunctions)
}

func (c *customerRoleCase) ownerRevokesFunctionGrant(t *testing.T) {
	mustExec(t, c.owner, "SELECT excalibase.create_role('rev_member', 'Rev-1', true, ARRAY['cust_group'])")
	mustExec(t, c.owner, "REVOKE cust_group FROM rev_member")
	if scanBool(t, c.admin, "SELECT pg_has_role('rev_member', 'cust_group', 'MEMBER')") {
		t.Fatal("the owner could not revoke a membership the function granted")
	}
	mustExec(t, c.owner, "SELECT excalibase.drop_role('rev_member')")
}

// A function in public that matches an unqualified call better than
// pg_catalog's would run as the superuser during a restore.
func (c *customerRoleCase) registrationIgnoresPlantedFunctions(t *testing.T) {
	mustExec(t, c.owner, `
CREATE FUNCTION public.convert_from(bytea, text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN EXECUTE 'ALTER ROLE `+customerOwner+` SUPERUSER'; RETURN pg_catalog.convert_from($1, $2::name); END $$;
CREATE FUNCTION public.format(text, text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN EXECUTE 'ALTER ROLE `+customerOwner+` SUPERUSER'; RETURN pg_catalog.format($1, $2); END $$;`)
	restore := c.spec
	restore.resetPasswords = true
	c.pg.psql(t, projectRoleSQL(restore, c.creds, "cdc_watcher_pub"))
	c.pg.psql(t, dropAllMongoUsersSQL())
	if scanBool(t, c.admin, "SELECT rolsuper FROM pg_roles WHERE rolname = $1", customerOwner) {
		t.Fatal("registration ran the owner's function as the superuser")
	}
}

// Postgres 16+ lets the owner create any free name; registration must not
// adopt a platform role that the owner administers.
func (c *customerRoleCase) registrationRefusesASquattedRole(t *testing.T) {
	mustExec(t, c.admin, "DROP ROLE "+config.DocumentDBBrowserLogin)
	mustExec(t, c.owner, "CREATE ROLE "+config.DocumentDBBrowserLogin+" LOGIN PASSWORD 'Squat-1'")
	if err := c.pg.psqlErr(projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub")); err == nil {
		t.Fatal("registration adopted a platform role the owner created")
	}
	mustExec(t, c.owner, "DROP ROLE "+config.DocumentDBBrowserLogin)
	mustExec(t, c.admin, "CREATE ROLE "+config.DocumentDBBrowserLogin+" LOGIN IN ROLE "+config.DocumentDBMongoUsersGroup)
}

func (c *customerRoleCase) ownerAttribute(t *testing.T) {
	if got := scanBool(t, c.admin, "SELECT rolcreaterole FROM pg_roles WHERE rolname = $1", customerOwner); got != c.createRole {
		t.Fatalf("owner CREATEROLE = %v, want %v", got, c.createRole)
	}
}

func (c *customerRoleCase) functionCreatesALoginRole(t *testing.T) {
	mustExec(t, c.owner, "SELECT excalibase.create_role('cust_reader', 'Cust-Pass-1')")
	mustExec(t, c.owner, "CREATE TABLE orders (id int PRIMARY KEY, total numeric); INSERT INTO orders VALUES (1, 9.5)")
	mustExec(t, c.owner, "GRANT SELECT ON orders TO cust_reader")
	reader := c.pg.connect(t, "cust_reader", "Cust-Pass-1")
	if got := scanString(t, reader, "SELECT total::text FROM orders WHERE id = 1"); got != "9.5" {
		t.Fatalf("customer role read %q", got)
	}
	expectPlainRole(t, c.admin, "cust_reader")
	if got := scanString(t, c.admin, "SELECT left(rolpassword, 14) FROM pg_authid WHERE rolname = 'cust_reader'"); got != "SCRAM-SHA-256$" {
		t.Fatalf("password stored as %q", got)
	}
	mustFail(t, reader, "UPDATE orders SET total = 0", "permission denied")
}

func (c *customerRoleCase) functionRefusesReservedNames(t *testing.T) {
	names := append(pgroles.ReservedNames(), "pg_x", "excalibase_x", "documentdb_x", "cnpg_x", "Excalibase_App",
		customerOwner, "cust_reader", mongoWriter, "other_role", strings.Repeat("a", 64))
	for _, name := range names {
		mustFailArgs(t, c.owner, "SELECT excalibase.create_role($1, 'P-1')", []any{name}, "")
	}
}

func (c *customerRoleCase) functionGrantsOnlyOwnedRoles(t *testing.T) {
	forbidden := append(protectedRoles(), "pg_execute_server_program", "pg_read_server_files",
		"pg_write_server_files", "pg_read_all_data", "pg_signal_backend", "pg_monitor")
	for _, role := range forbidden {
		mustFailArgs(t, c.owner, "SELECT excalibase.create_role('escalate', 'P-1', true, ARRAY[$1])", []any{role}, "")
	}
	if exists(t, c.admin, "escalate") {
		t.Fatal("a refused create left its role behind")
	}
	mustExec(t, c.owner, "SELECT excalibase.create_role('cust_group', NULL, false)")
	mustExec(t, c.owner, "SELECT excalibase.create_role('cust_member', 'Member-1', true, ARRAY['cust_group'])")
	if !scanBool(t, c.admin, "SELECT pg_has_role('cust_member', 'cust_group', 'MEMBER')") {
		t.Fatal("cust_member is not in cust_group")
	}
	if scanBool(t, c.admin, "SELECT rolcanlogin FROM pg_roles WHERE rolname = 'cust_group'") {
		t.Fatal("cust_group can log in")
	}
}

func (c *customerRoleCase) functionManagesOnlyOwnedRoles(t *testing.T) {
	for _, role := range protectedRoles() {
		mustFailArgs(t, c.owner, "SELECT excalibase.alter_role_password($1, 'Taken-1')", []any{role}, "")
		mustFailArgs(t, c.owner, "SELECT excalibase.drop_role($1)", []any{role}, "")
		if !exists(t, c.admin, role) {
			t.Fatalf("%s was dropped", role)
		}
	}
	c.pg.assertLogin(t, roleApp, customerAppPassword)
	c.pg.assertLogin(t, mongoWriter, "Mongo-Pass-1")
	mustExec(t, c.owner, "SELECT excalibase.alter_role_password('cust_member', 'Member-2')")
	c.pg.assertLogin(t, "cust_member", "Member-2")
	mustExec(t, c.owner, "SELECT excalibase.drop_role('cust_member')")
	if exists(t, c.admin, "cust_member") {
		t.Fatal("cust_member was not dropped")
	}
}

func (c *customerRoleCase) namesAreQuoted(t *testing.T) {
	hostile := `we"ird'; DROP ROLE postgres; --`
	password := `p'$$; CREATE ROLE pwned SUPERUSER; --`
	mustExecArgs(t, c.owner, "SELECT excalibase.create_role($1, $2)", hostile, password)
	c.pg.assertLogin(t, hostile, password)
	c.pg.assertNoInjectedEffects(t)
	mustExecArgs(t, c.owner, "SELECT excalibase.drop_role($1)", hostile)
}

func (c *customerRoleCase) preHashedPassword(t *testing.T) {
	verifier, err := pgscram.Verifier("Unicode-\u00e9\u00df-\u30d1\u30b9")
	if err != nil {
		t.Fatal(err)
	}
	mustExecArgs(t, c.owner, "SELECT excalibase.create_role('cust_hashed', $1)", verifier)
	c.pg.assertLogin(t, "cust_hashed", "Unicode-\u00e9\u00df-\u30d1\u30b9")
}

func (c *customerRoleCase) onlyOwnerAndAppMayCall(t *testing.T) {
	reader := c.pg.connect(t, "cust_reader", "Cust-Pass-1")
	mustFail(t, reader, "SELECT excalibase.create_role('by_reader', 'P-1')", "permission denied")
	mustFail(t, c.pg.connect(t, mongoWriter, "Mongo-Pass-1"), "SELECT excalibase.drop_role('cust_reader')", "permission denied")
	app := c.pg.connect(t, roleApp, customerAppPassword)
	mustExec(t, app, "SELECT excalibase.create_role('studio_made', 'Studio-1')")
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
		JOIN pg_roles o ON o.oid = m.member WHERE r.rolname = 'studio_made' AND o.rolname = $1 AND m.admin_option)`, customerOwner) {
		t.Fatal("a role Studio created is not the owner's to manage")
	}
	mustExec(t, c.owner, "SELECT excalibase.drop_role('studio_made')")
}

func (c *customerRoleCase) ownerHasNoCreateRole(t *testing.T) {
	mustFail(t, c.owner, "CREATE ROLE direct LOGIN", "permission denied")
	mustFail(t, c.owner, "GRANT pg_execute_server_program TO "+customerOwner, "")
}

func (c *customerRoleCase) restoreLogicKeepsCustomerRoles(t *testing.T) {
	c.pg.psql(t, dropAllMongoUsersSQL())
	if exists(t, c.admin, mongoWriter) || !exists(t, c.admin, "cust_reader") || !exists(t, c.admin, "cust_group") {
		t.Fatal("Mongo cleanup touched the wrong roles")
	}
	restore := c.spec
	restore.resetPasswords = true
	c.pg.psql(t, projectRoleSQL(restore, c.creds, "cdc_watcher_pub"))
	c.pg.assertLogin(t, "cust_reader", "Cust-Pass-1")
	mustExec(t, c.owner, "SELECT excalibase.alter_role_password('cust_reader', 'Cust-Pass-2')")
	c.pg.assertLogin(t, "cust_reader", "Cust-Pass-2")
}

// The scenarios below are Postgres 16+: the owner's own CREATEROLE reaches
// only roles it created.

func (c *customerRoleCase) ownerManagesRolesInSQL(t *testing.T) {
	mustExec(t, c.owner, "CREATE ROLE sql_role LOGIN PASSWORD 'Sql-Pass-1'")
	c.pg.assertLogin(t, "sql_role", "Sql-Pass-1")
	mustExec(t, c.owner, "GRANT SELECT ON orders TO sql_role")
	mustExec(t, c.owner, "ALTER ROLE sql_role PASSWORD 'Sql-Pass-2'")
	c.pg.assertLogin(t, "sql_role", "Sql-Pass-2")
	mustExec(t, c.owner, "SELECT excalibase.alter_role_password('sql_role', 'Sql-Pass-3')")
	c.pg.assertLogin(t, "sql_role", "Sql-Pass-3")
}

func (c *customerRoleCase) ownerCannotGrantPredefinedRoles(t *testing.T) {
	for _, role := range predefinedRoles(t, c.admin) {
		mustFail(t, c.owner, fmt.Sprintf("GRANT %s TO sql_role", pq.QuoteIdentifier(role)), "")
		mustFail(t, c.owner, fmt.Sprintf("GRANT %s TO %s", pq.QuoteIdentifier(role), customerOwner), "")
	}
}

func (c *customerRoleCase) ownerCannotTouchOtherRoles(t *testing.T) {
	for _, role := range protectedRoles() {
		for _, stmt := range statementsOnAnotherRole(role) {
			mustFail(t, c.owner, stmt, "")
		}
	}
	mustFail(t, c.owner, "CREATE ROLE sneaky IN ROLE "+config.DocumentDBMongoUsersGroup, "")
	c.pg.assertLogin(t, roleApp, customerAppPassword)
}

// statementsOnAnotherRole are what CREATEROLE would allow on a role the owner
// held ADMIN OPTION on. Any role may change its own password, the owner too.
func statementsOnAnotherRole(role string) []string {
	quoted := pq.QuoteIdentifier(role)
	statements := []string{
		"ALTER ROLE " + quoted + " NOLOGIN",
		"ALTER ROLE " + quoted + " RENAME TO renamed_role",
		"GRANT " + quoted + " TO sql_role",
		"DROP ROLE " + quoted,
		"CREATE ROLE " + quoted,
	}
	if role != customerOwner {
		statements = append(statements, "ALTER ROLE "+quoted+" PASSWORD 'Taken-1'")
	}
	return statements
}

func (c *customerRoleCase) ownerCannotCreatePrivilegedRoles(t *testing.T) {
	for _, attribute := range []string{"SUPERUSER", "REPLICATION", "BYPASSRLS", "CREATEDB"} {
		mustFail(t, c.owner, "CREATE ROLE privileged "+attribute, "")
		mustFail(t, c.owner, "ALTER ROLE sql_role "+attribute, "")
	}
	mustFail(t, c.owner, "ALTER ROLE "+customerOwner+" BYPASSRLS", "")
}

func (c *customerRoleCase) ownerDropsItsRole(t *testing.T) {
	mustExec(t, c.owner, "REVOKE SELECT ON orders FROM sql_role; DROP ROLE sql_role")
	if exists(t, c.admin, "sql_role") {
		t.Fatal("sql_role was not dropped")
	}
}

// customerClusterSetupSQL is what CloudNativePG's initdb and the DocumentDB
// bootstrap leave behind, without the extension itself.
func customerClusterSetupSQL() string {
	return strings.Join([]string{
		"CREATE ROLE " + customerOwner + " LOGIN PASSWORD '" + customerOwnerPassword + "'",
		"ALTER DATABASE app OWNER TO " + customerOwner,
		"CREATE ROLE streaming_replica LOGIN REPLICATION",
		"CREATE ROLE other_role LOGIN PASSWORD 'Other-1'",
		"CREATE ROLE " + config.DocumentDBGatewayRole + " LOGIN",
		"CREATE ROLE documentdb_admin_role NOLOGIN",
		"GRANT documentdb_admin_role TO " + customerOwner + ", " + config.DocumentDBGatewayRole,
		"CREATE ROLE " + config.DocumentDBMongoUsersGroup + " NOLOGIN",
		"CREATE ROLE " + mongoWriter + " LOGIN PASSWORD 'Mongo-Pass-1' IN ROLE " + config.DocumentDBMongoUsersGroup + ", documentdb_admin_role",
		"CREATE ROLE " + config.DocumentDBBrowserLogin + " LOGIN PASSWORD 'Browser-1' IN ROLE " + config.DocumentDBMongoUsersGroup,
	}, ";\n")
}

func expectPlainRole(t *testing.T, db *sql.DB, role string) {
	t.Helper()
	if scanBool(t, db, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls
		FROM pg_roles WHERE rolname = $1`, role) {
		t.Fatalf("%s carries a privileged attribute", role)
	}
}

func predefinedRoles(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT rolname FROM pg_roles WHERE rolname LIKE 'pg\_%' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, role)
	}
	if len(roles) < 10 {
		t.Fatalf("only %d predefined roles: %v", len(roles), roles)
	}
	return roles
}

func exists(t *testing.T, db *sql.DB, role string) bool {
	t.Helper()
	return scanBool(t, db, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role)
}

func scanBool(t *testing.T, db *sql.DB, query string, args ...any) bool {
	t.Helper()
	var value bool
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

func scanString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

func mustExec(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	mustExecArgs(t, db, statement)
}

func mustExecArgs(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("%s %v: %v", statement, args, err)
	}
}

func mustFail(t *testing.T, db *sql.DB, statement, want string) {
	t.Helper()
	mustFailArgs(t, db, statement, nil, want)
}

// mustFailArgs expects a refusal, and one mentioning want when it is set.
func mustFailArgs(t *testing.T, db *sql.DB, statement string, args []any, want string) {
	t.Helper()
	_, err := db.Exec(statement, args...)
	if err == nil {
		t.Fatalf("allowed: %s %v", statement, args)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("%s %v refused for another reason: %v", statement, args, err)
	}
}
