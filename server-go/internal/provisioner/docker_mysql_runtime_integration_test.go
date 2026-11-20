//go:build integration

package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-765 on a real single-host mysqld: the runtime login works the project's
// own tables (create, migrate, read, write) and cannot create a user, grant a
// privilege, or reach the platform's databases; neither can the owner's login.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerMySQLRuntime -v
func TestDockerMySQLRuntimeLoginCannotManageUsers(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectID := fmt.Sprintf("it-myrt-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerMySQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "shop"}
	result, err := p.Provision(ctx, req, config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), MySQLContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := MySQLPasswords{Admin: "Adm1n-" + projectID, App: "app-" + projectID, AuthAdmin: "auth-" + projectID,
		Watcher: "cdc-" + projectID, Runtime: "rt-" + projectID}
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	verified := hostTLS(ctx, t, p, result.Namespace, "localhost")

	runtime := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{MySQLUserRuntime, passwords.Runtime, "shop"}, verified)
	defer runtime.Close()
	runtimeWorksItsOwnTables(ctx, t, runtime)

	admin := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, verified)
	defer admin.Close()
	loginsAreRefusedUserManagement(ctx, t, map[string]*sql.DB{"runtime": runtime, "owner": admin})
	runtimeHoldsNoGlobalGrant(ctx, t, runtime)

	// Without a runtime password, a runtime login the database holds is locked.
	passwords.Runtime = ""
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers without a runtime login: %v", err)
	}
	locked := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{MySQLUserRuntime, "rt-" + projectID, "shop"}, verified)
	defer locked.Close()
	if err := locked.PingContext(ctx); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a locked runtime login signed in: %v", err)
	}
}

func runtimeWorksItsOwnTables(ctx context.Context, t *testing.T, runtime *sql.DB) {
	t.Helper()
	for _, allowed := range []string{
		"CREATE TABLE notes (id INT PRIMARY KEY, body TEXT)",
		"INSERT INTO notes VALUES (1, 'hello')",
		"ALTER TABLE notes ADD COLUMN created_at DATETIME NULL",
		"CREATE INDEX notes_body ON notes (body(32))",
		"UPDATE notes SET body = 'again' WHERE id = 1",
	} {
		mustExec(ctx, t, runtime, allowed)
	}
	var body string
	if err := runtime.QueryRowContext(ctx, "SELECT body FROM notes WHERE id = 1").Scan(&body); err != nil || body != "again" {
		t.Fatalf("the runtime login read %q: %v", body, err)
	}
}

// loginsAreRefusedUserManagement runs what would mint or change a login, or
// reach the platform's databases, as each login.
func loginsAreRefusedUserManagement(ctx context.Context, t *testing.T, logins map[string]*sql.DB) {
	t.Helper()
	for _, refused := range []string{
		"CREATE USER 'planted'@'%' IDENTIFIED BY 'x'",
		"ALTER USER 'excalibase_app'@'%' IDENTIFIED BY 'x'",
		"GRANT ALL PRIVILEGES ON *.* TO 'excalibase_runtime'@'%'",
		"GRANT SELECT ON shop.* TO 'excalibase_reader'@'%'",
		"SET GLOBAL read_only = 1",
		"SELECT COUNT(*) FROM auth.users",
		"CREATE TABLE excalibase.planted (id INT)",
	} {
		for who, db := range logins {
			if _, err := db.ExecContext(ctx, refused); err == nil {
				t.Errorf("the %s login ran %s", who, refused)
			}
		}
	}
}

func runtimeHoldsNoGlobalGrant(ctx context.Context, t *testing.T, runtime *sql.DB) {
	t.Helper()
	rows, err := runtime.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		t.Fatalf("SHOW GRANTS: %v", err)
	}
	defer rows.Close()
	var grants strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		grants.WriteString(line + "\n")
	}
	held := strings.ReplaceAll(grants.String(), "GRANT USAGE ON *.*", "")
	for _, forbidden := range []string{"GRANT OPTION", "CREATE USER", "SUPER", "ON *.*"} {
		if strings.Contains(held, forbidden) {
			t.Errorf("the runtime login holds %s:\n%s", forbidden, grants.String())
		}
	}
}

// EXC-765 review: a database named "aut_" must not reach "auth" through the
// pattern a plain grant makes of its name, including a grant an earlier run left.
func TestDockerMySQLWildcardDatabaseNameDoesNotReachAuth(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectID := fmt.Sprintf("it-mywc-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerMySQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "aut_"}
	result, err := p.Provision(ctx, req, config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), MySQLContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := MySQLPasswords{Admin: "Adm1n-" + projectID, App: "app-" + projectID, AuthAdmin: "auth-" + projectID,
		Watcher: "cdc-" + projectID, Runtime: "rt-" + projectID}
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "aut_", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	if _, err := p.run(ctx, result.Namespace, "CREATE TABLE auth.users (id INT, hash TEXT); INSERT INTO auth.users VALUES (1, 'secret-hash');"); err != nil {
		t.Fatalf("seed auth.users: %v", err)
	}
	// What an earlier run granted: the name as a pattern.
	if _, err := p.run(ctx, result.Namespace, "GRANT ALL PRIVILEGES ON `aut_`.* TO 'admin'@'%', 'excalibase_runtime'@'%';"); err != nil {
		t.Fatalf("legacy grant: %v", err)
	}
	verified := hostTLS(ctx, t, p, result.Namespace, "localhost")
	probe := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{MySQLUserAdmin, passwords.Admin, "aut_"}, verified)
	if _, err := probe.ExecContext(ctx, "SELECT hash FROM auth.users"); err != nil {
		t.Fatalf("precondition: the pattern grant does not reach auth, so this proves nothing: %v", err)
	}
	probe.Close()

	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "aut_", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers again: %v", err)
	}
	for user, password := range map[string]string{MySQLUserAdmin: passwords.Admin, MySQLUserRuntime: passwords.Runtime} {
		db := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{user, password, "aut_"}, verified)
		if _, err := db.ExecContext(ctx, "SELECT hash FROM auth.users"); err == nil {
			t.Errorf("%s still reads auth.users through the database name", user)
		}
		mustExec(ctx, t, db, "CREATE TABLE IF NOT EXISTS own_table (id INT)")
		mustExec(ctx, t, db, "INSERT INTO own_table VALUES (1)")
		db.Close()
	}
}
