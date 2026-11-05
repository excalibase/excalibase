//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-675 on a real single-host mysqld: the reader reads the project
// database and writes nothing, and ending a user's sessions drops what that
// user held while the others keep theirs.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerMySQLReader -v
func TestDockerMySQLReaderReadsOnlyAndSessionsEnd(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectID := fmt.Sprintf("it-myrd-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerMySQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "shop"}
	result, err := p.Provision(ctx, req, config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), MySQLContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := MySQLPasswords{Admin: "Adm1n-" + projectID, App: "app-" + projectID, AuthAdmin: "auth-" + projectID,
		Watcher: "cdc-" + projectID, Reader: "rd-" + projectID}
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	verified := hostTLS(ctx, t, p, result.Namespace, "localhost")
	admin := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, verified)
	defer admin.Close()
	mustExec(ctx, t, admin, "CREATE TABLE notes (id INT PRIMARY KEY, body TEXT)")
	mustExec(ctx, t, admin, "INSERT INTO notes VALUES (1, 'hello')")

	reader := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{MySQLUserReader, passwords.Reader, "shop"}, verified)
	defer reader.Close()
	var body string
	if err := reader.QueryRowContext(ctx, "SELECT body FROM notes WHERE id = 1").Scan(&body); err != nil || body != "hello" {
		t.Fatalf("the reader read %q: %v", body, err)
	}
	for _, refused := range []string{
		"INSERT INTO notes VALUES (2, 'x')", "UPDATE notes SET body = 'x'", "DELETE FROM notes",
		"CREATE TABLE planted (id INT)", "DROP TABLE notes", "SELECT COUNT(*) FROM auth.users",
	} {
		if _, err := reader.ExecContext(ctx, refused); err == nil {
			t.Errorf("the reader ran %s", refused)
		}
	}

	held, err := admin.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer held.Close()
	readerHeld, err := reader.Conn(ctx)
	if err != nil {
		t.Fatalf("reader conn: %v", err)
	}
	defer readerHeld.Close()
	if err := p.EndUserSessions(ctx, result.Namespace, projectID, MySQLUserAdmin); err != nil {
		t.Fatalf("EndUserSessions: %v", err)
	}
	if err := held.PingContext(ctx); err == nil {
		t.Fatal("the admin session survived")
	}
	if err := readerHeld.PingContext(ctx); err != nil {
		t.Fatalf("another user's session was ended: %v", err)
	}

	// A replica waits on what the primary applied; on one server that answers at once.
	executed, err := p.run(ctx, result.Namespace, MySQLExecutedGTIDSQL)
	if err != nil {
		t.Fatalf("read GTIDs: %v", err)
	}
	await, err := MySQLAwaitGTIDSQL(strings.TrimSpace(executed))
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if out, err := p.run(ctx, result.Namespace, await); err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("await answered %q: %v", out, err)
	}

	// Without a reader password, a reader the database holds is locked.
	passwords.Reader = ""
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers without a reader: %v", err)
	}
	locked := mysqlAs(ctx, t, docker, result.Namespace, mysqlLogin{MySQLUserReader, "rd-" + projectID, "shop"}, verified)
	defer locked.Close()
	if err := locked.PingContext(ctx); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a locked reader signed in: %v", err)
	}
}
