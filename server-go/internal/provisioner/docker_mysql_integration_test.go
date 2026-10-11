//go:build integration

package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
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

	db := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, "skip-verify")
	mustExec(ctx, t, db, "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)")
	mustExec(ctx, t, db, "INSERT INTO notes (body) VALUES ('hello'), ('world')")
	_ = db.Close()

	plain := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, "false")
	if err := plain.PingContext(ctx); err == nil {
		t.Fatal("a client without TLS was let in")
	}
	_ = plain.Close()

	if err := p.Pause(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if stopped, err := p.WorkloadStopped(ctx, result.Namespace, projectID); err != nil || !stopped {
		t.Fatalf("after pause stopped=%v err=%v", stopped, err)
	}
	if err := p.Resume(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	db = mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, "skip-verify")
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

// mysqlFromHost opens the project's published port as admin with tls.
func mysqlFromHost(ctx context.Context, t *testing.T, docker *RealDockerClient, container, password, tls string) *sql.DB {
	t.Helper()
	state, err := docker.ContainerState(ctx, container)
	if err != nil || state.HostPorts["3306"] == 0 {
		t.Fatalf("published port of %s: %+v %v", container, state, err)
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = MySQLUserAdmin, password, "shop"
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.HostPorts["3306"]))
	cfg.TLSConfig = tls
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
	db := mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, "skip-verify")
	mustExec(ctx, t, db, "CREATE TABLE notes (id INT PRIMARY KEY)")
	mustExec(ctx, t, db, "INSERT INTO notes VALUES (1), (2)")
	_ = db.Close()

	newest, _ := config.MySQLImage("8.4.10")
	if err := p.ReplaceImage(ctx, projectID, newest, plan); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}
	db = mysqlFromHost(ctx, t, docker, result.Namespace, passwords.Admin, "skip-verify")
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
