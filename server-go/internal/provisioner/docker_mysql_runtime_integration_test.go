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
