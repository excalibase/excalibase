//go:build integration

package provisioner

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/mysqltls"
)

// A MySQL project on a real engine (EXC-592): provisioned, its platform users
// created, reached over TLS from the host, refused without TLS, paused,
// resumed with its rows, and deleted.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerMySQL -v
func TestDockerMySQLProjectServesTLSClientsFromTheHost(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectID := fmt.Sprintf("it-mysql-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerMySQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "shop"}
	result, err := p.Provision(ctx, req, config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), MySQLContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := MySQLPasswords{Admin: "Adm1n-" + projectID, App: "app-" + projectID, AuthAdmin: "auth-" + projectID, Watcher: "cdc-" + projectID}
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}

	verified := hostTLS(ctx, t, p, result.Namespace, "localhost")
	db := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, verified)
	mustExec(ctx, t, db, "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)")
	mustExec(ctx, t, db, "INSERT INTO notes (body) VALUES ('hello'), ('world')")
	mustExec(ctx, t, db, "CREATE TRIGGER notes_body BEFORE INSERT ON notes FOR EACH ROW SET NEW.body = UPPER(NEW.body)")
	mustExec(ctx, t, db, "CREATE FUNCTION twice(n INT) RETURNS INT DETERMINISTIC RETURN n * 2")
	var doubled int
	if err := db.QueryRowContext(ctx, "SELECT twice(21)").Scan(&doubled); err != nil || doubled != 42 {
		t.Fatalf("twice(21)=%d err=%v", doubled, err)
	}
	// EXC-626: the customer's login cannot read the end users' auth database.
	if _, err := db.ExecContext(ctx, "SELECT COUNT(*) FROM auth.users"); err == nil {
		t.Fatal("admin reached the auth database")
	}
	_ = db.Close()
	assertAuthAdminIsConfinedToAuth(ctx, t, docker, result.Namespace, passwords.AuthAdmin, verified)

	plain := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, nil)
	if err := plain.PingContext(ctx); err == nil {
		t.Fatal("a client without TLS was let in")
	}
	_ = plain.Close()
	// EXC-589: the server is verified, so another CA or another name is refused.
	foreign := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, hostTLS(ctx, t, p, result.Namespace, "db.example.com"))
	if err := foreign.PingContext(ctx); err == nil {
		t.Fatal("a certificate that does not name the dialled host was accepted")
	}
	_ = foreign.Close()

	if err := p.Pause(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if stopped, err := p.WorkloadStopped(ctx, result.Namespace, projectID); err != nil || !stopped {
		t.Fatalf("after pause stopped=%v err=%v", stopped, err)
	}
	if err := p.Resume(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	db = mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, verified)
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notes").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("after resume rows=%d err=%v", rows, err)
	}
	_ = db.Close()
	if work, err := p.ClientWork(ctx, result.Namespace, projectID); err != nil || work == 0 {
		t.Fatalf("client work %d err %v", work, err)
	}

	if err := p.Deprovision(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if status, _ := docker.ContainerStatus(ctx, result.Namespace); status != containerNotFound {
		t.Fatalf("container still %s", status)
	}
}

// hostTLS verifies the project's server against its CA, as serverName.
func hostTLS(ctx context.Context, t *testing.T, p *DockerMySQLProvisioner, container, serverName string) *tls.Config {
	t.Helper()
	ca, err := p.ServerCA(ctx, container, "")
	if err != nil {
		t.Fatalf("ServerCA: %v", err)
	}
	config, err := mysqltls.ClientConfig(ca, serverName)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// mysqlFromHost opens the project's published port as admin over tlsConfig, or
// without TLS when it is nil.
func mysqlFromHost(ctx context.Context, t *testing.T, docker *RealDockerClient, container, password string, tlsConfig *tls.Config) *sql.DB {
	t.Helper()
	return mysqlAs(ctx, t, docker, container, MySQLUserAdmin, password, "shop", tlsConfig)
}

// assertAuthAdminIsConfinedToAuth: auth_admin builds and uses the auth
// service's tables, and holds nothing else (EXC-626).
func assertAuthAdminIsConfinedToAuth(ctx context.Context, t *testing.T, docker *RealDockerClient, container, password string, tlsConfig *tls.Config) {
	t.Helper()
	db := mysqlAs(ctx, t, docker, container, MySQLUserAuthAdmin, password, MySQLAuthDatabase, tlsConfig)
	defer db.Close()
	mustExec(ctx, t, db, "CREATE TABLE users (id BIGINT AUTO_INCREMENT PRIMARY KEY, email VARCHAR(100) NOT NULL UNIQUE)")
	mustExec(ctx, t, db, "CREATE TABLE tokens (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL, FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE)")
	mustExec(ctx, t, db, "ALTER TABLE users ADD COLUMN role VARCHAR(63) NOT NULL DEFAULT 'user'")
	mustExec(ctx, t, db, "CREATE INDEX idx_users_role ON users (role)")
	mustExec(ctx, t, db, "INSERT INTO users (email) VALUES ('a@b.test')")
	mustExec(ctx, t, db, "UPDATE users SET role = 'editor'")
	mustExec(ctx, t, db, "DELETE FROM users")
	mustExec(ctx, t, db, "DROP TABLE tokens")
	for _, refused := range []string{
		"CREATE PROCEDURE p() SELECT 1",
		"CREATE VIEW v AS SELECT 1",
		"CREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW SET NEW.role = 'x'",
		"SELECT COUNT(*) FROM shop.notes",
		"SELECT COUNT(*) FROM mysql.user",
	} {
		if _, err := db.ExecContext(ctx, refused); err == nil {
			t.Errorf("auth_admin was allowed: %s", refused)
		}
	}
}

func mysqlAs(ctx context.Context, t *testing.T, docker *RealDockerClient, container, user, password, database string, tlsConfig *tls.Config) *sql.DB {
	t.Helper()
	state, err := docker.ContainerState(ctx, container)
	if err != nil || state.HostPorts["3306"] == 0 {
		t.Fatalf("published port of %s: %+v %v", container, state, err)
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = user, password, database
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.HostPorts["3306"]))
	cfg.TLS = tlsConfig
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	return sql.OpenDB(connector)
}

func mustExec(ctx context.Context, t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// EXC-592: a minor upgrade opens the same data on the newer image.
func TestDockerMySQLMinorUpgradeKeepsTheRows(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	projectID := fmt.Sprintf("it-myup-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerMySQLProvisioner(docker)
	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "shop", MySQLVersion: "8.4.9"}
	result, err := p.Provision(ctx, req, plan, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), MySQLContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := MySQLPasswords{Admin: "Adm1n-" + projectID, App: "app-" + projectID, AuthAdmin: "auth-" + projectID, Watcher: "cdc-" + projectID}
	if err := p.EnsurePlatformUsers(ctx, result.Namespace, projectID, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	caBefore, err := p.ServerCA(ctx, result.Namespace, projectID)
	if err != nil {
		t.Fatal(err)
	}
	db := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, hostTLS(ctx, t, p, result.Namespace, "localhost"))
	mustExec(ctx, t, db, "CREATE TABLE notes (id INT PRIMARY KEY)")
	mustExec(ctx, t, db, "INSERT INTO notes VALUES (1), (2)")
	_ = db.Close()

	newest, _ := config.MySQLImage("8.4.10")
	if err := p.ReplaceImage(ctx, projectID, newest, plan); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}
	// The recreated container serves the same CA from the volume (EXC-589).
	if caAfter, err := p.ServerCA(ctx, result.Namespace, projectID); err != nil || caAfter != caBefore {
		t.Fatalf("the CA changed across the upgrade (err %v)", err)
	}
	db = mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, hostTLS(ctx, t, p, result.Namespace, "localhost"))
	defer db.Close()
	var version string
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT VERSION(), (SELECT COUNT(*) FROM notes)").Scan(&version, &rows); err != nil || version != "8.4.10" || rows != 2 {
		t.Fatalf("after the upgrade: version %q rows %d err %v", version, rows, err)
	}
	if err := p.Deprovision(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
}
