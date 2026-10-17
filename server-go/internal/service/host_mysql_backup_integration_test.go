//go:build integration

package service

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
	"github.com/excalibase/provisioning-poc/internal/mysqltls"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-592 on a real engine: a single host's MySQL project is backed up, then
// restored to a new project at a second between two writes, and in place to
// its first backup; logins come back with the dump.
//
// Run: go test -tags=integration ./internal/service/ -run TestHostMySQLBackup -v
func TestHostMySQLBackupRestoresToAPointAndInPlace(t *testing.T) {
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	suffix := time.Now().UnixNano() % 1_000_000
	source, restoredID := fmt.Sprintf("it-mys-%d", suffix), fmt.Sprintf("it-myc-%d", suffix)
	mysqlProv := provisioner.NewDockerMySQLProvisioner(docker)
	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	result, err := mysqlProv.Provision(ctx, domain.ProvisioningRequest{ProjectName: source, DBType: domain.MySQL, DatabaseName: "shop"}, plan, func(domain.ProvisioningStage) {})
	for _, project := range []string{source, restoredID} {
		t.Cleanup(func() { _ = mysqlProv.RemoveData(context.Background(), project) })
	}
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := provisioner.MySQLPasswords{Admin: "Adm1n-" + source, App: "app-" + source, AuthAdmin: "auth-" + source, Watcher: "cdc-" + source}
	if err := mysqlProv.EnsurePlatformUsers(ctx, result.Namespace, source, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	inst := &domain.DatabaseInstance{ProjectID: source, OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker,
		Tier: domain.Free, Namespace: result.Namespace, Host: result.Host, DatabaseName: "shop", MySQLVersion: config.DefaultMySQLVersion()}
	instances := emptyInstanceStore(t)
	if err := instances.Create(inst); err != nil {
		t.Fatal(err)
	}
	adapter := NewHostMySQLBackupAdapter(HostMySQLBackupAdapterConfig{
		Docker: docker, MySQL: mysqlProv, Streamer: NewDockerStreamer(docker.RawClient()), Uploader: newFakeS3Uploader(),
		Records: &fakeBackupRecordStore{}, Bucket: "it", KeyPrefix: "backups/", Instances: instances,
	})
	adapter.SetProjectRegistrar(&fakeRegistrar{store: instances})
	adapter.SetDatabaseProbe(alwaysAnswers{})
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: plan}})

	db := hostMySQL(ctx, t, docker, result.Namespace, passwords.Admin)
	execAll(ctx, t, db, "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)", insertNotes(100))
	first, err := adapter.TriggerManual(ctx, inst)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	execAll(ctx, t, db, insertNotes(50))
	time.Sleep(1100 * time.Millisecond)
	point := time.Now().UTC().Truncate(time.Second)
	time.Sleep(1100 * time.Millisecond)
	execAll(ctx, t, db, insertNotes(50))
	_ = db.Close()
	if _, err := adapter.RunScheduled(ctx, inst); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	restored, err := adapter.Restore(ctx, inst, domain.RestoreRequest{NewProjectName: restoredID, TargetProjectID: restoredID, TargetTime: &domain.ZonedTime{Time: point}})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rows := countNotes(ctx, t, docker, restored.Host, passwords.Admin); rows != 150 {
		t.Fatalf("restored to %s: %d rows, want 150", point, rows)
	}

	target, err := adapter.InPlaceRecoveryTarget(ctx, inst, domain.RestoreRequest{BackupID: first.ID})
	if err != nil {
		t.Fatalf("in-place target: %v", err)
	}
	if err := adapter.ReplaceDatabase(ctx, inst, target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	if rows := countNotes(ctx, t, docker, inst.Namespace, passwords.Admin); rows != 100 {
		t.Fatalf("in place: %d rows, want 100", rows)
	}
	if due, err := adapter.BackupDue(ctx, inst); err != nil || !due {
		t.Fatalf("a rebuilt server is due a backup: %v %v", due, err)
	}
}

// EXC-587 on a real engine: between backups the binlogs are shipped, so a
// restore reaches a second after the last backup and the last ship itself.
//
// Run: go test -tags=integration ./internal/service/ -run TestHostMySQLShipped -v
func TestHostMySQLShippedBinlogsReachPastTheLastBackup(t *testing.T) {
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	suffix := time.Now().UnixNano() % 1_000_000
	source, atPoint, atLatest := fmt.Sprintf("it-mss-%d", suffix), fmt.Sprintf("it-msp-%d", suffix), fmt.Sprintf("it-msl-%d", suffix)
	mysqlProv := provisioner.NewDockerMySQLProvisioner(docker)
	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	result, err := mysqlProv.Provision(ctx, domain.ProvisioningRequest{ProjectName: source, DBType: domain.MySQL, DatabaseName: "shop"}, plan, func(domain.ProvisioningStage) {})
	for _, project := range []string{source, atPoint, atLatest} {
		t.Cleanup(func() { _ = mysqlProv.RemoveData(context.Background(), project) })
	}
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := provisioner.MySQLPasswords{Admin: "Adm1n-" + source, App: "app-" + source, AuthAdmin: "auth-" + source, Watcher: "cdc-" + source}
	if err := mysqlProv.EnsurePlatformUsers(ctx, result.Namespace, source, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	inst := &domain.DatabaseInstance{ProjectID: source, OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker,
		Tier: domain.Free, Namespace: result.Namespace, Host: result.Host, DatabaseName: "shop", MySQLVersion: config.DefaultMySQLVersion()}
	instances := emptyInstanceStore(t)
	if err := instances.Create(inst); err != nil {
		t.Fatal(err)
	}
	adapter := NewHostMySQLBackupAdapter(HostMySQLBackupAdapterConfig{
		Docker: docker, MySQL: mysqlProv, Streamer: NewDockerStreamer(docker.RawClient()), Uploader: newFakeS3Uploader(),
		Records: &fakeBackupRecordStore{}, Bucket: "it", KeyPrefix: "backups/", Instances: instances,
	})
	adapter.SetProjectRegistrar(&fakeRegistrar{store: instances})
	adapter.SetDatabaseProbe(alwaysAnswers{})
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: plan}})

	db := hostMySQL(ctx, t, docker, result.Namespace, passwords.Admin)
	execAll(ctx, t, db, "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)", insertNotes(100))
	if _, err := adapter.TriggerManual(ctx, inst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	execAll(ctx, t, db, insertNotes(50))
	time.Sleep(1100 * time.Millisecond)
	point := time.Now().UTC().Truncate(time.Second)
	time.Sleep(1100 * time.Millisecond)
	execAll(ctx, t, db, insertNotes(25))
	time.Sleep(1100 * time.Millisecond)
	if err := adapter.ShipBinlogs(ctx, inst); err != nil {
		t.Fatalf("ship: %v", err)
	}
	execAll(ctx, t, db, insertNotes(10))
	_ = db.Close()

	window, err := adapter.RestoreWindow(ctx, inst)
	if err != nil || window == nil || window.Latest.Before(point) {
		t.Fatalf("window %+v %v does not reach past the backup", window, err)
	}
	for _, restore := range []struct {
		project string
		at      time.Time
		rows    int
	}{{atPoint, point, 150}, {atLatest, window.Latest, 175}} {
		restored, err := adapter.Restore(ctx, inst, domain.RestoreRequest{NewProjectName: restore.project, TargetProjectID: restore.project,
			TargetTime: &domain.ZonedTime{Time: restore.at}})
		if err != nil {
			t.Fatalf("Restore at %s: %v", restore.at, err)
		}
		if rows := countNotes(ctx, t, docker, restored.Host, passwords.Admin); rows != restore.rows {
			t.Fatalf("restored to %s: %d rows, want %d", restore.at, rows, restore.rows)
		}
	}
	late := &domain.ZonedTime{Time: window.Latest.Add(time.Second)}
	if err := adapter.CheckRestoreTarget(ctx, inst, domain.RestoreRequest{TargetTime: late}); err == nil {
		t.Fatal("a point after the last ship was accepted")
	}
}

func insertNotes(count int) string {
	return fmt.Sprintf("INSERT INTO notes (body) SELECT 'note' FROM (WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d) SELECT i FROM n) rows_", count)
}

func execAll(ctx context.Context, t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func countNotes(ctx context.Context, t *testing.T, docker *provisioner.RealDockerClient, container, password string) int {
	t.Helper()
	db := hostMySQL(ctx, t, docker, container, password)
	defer db.Close()
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notes").Scan(&rows); err != nil {
		t.Fatalf("count in %s: %v", container, err)
	}
	return rows
}

// hostMySQL opens the container's published port as admin over TLS,
// verifying the server against the container's own CA (EXC-589).
func hostMySQL(ctx context.Context, t *testing.T, docker *provisioner.RealDockerClient, container, password string) *sql.DB {
	t.Helper()
	state, err := docker.ContainerState(ctx, container)
	if err != nil || state.HostPorts["3306"] == 0 {
		t.Fatalf("published port of %s: %+v %v", container, state, err)
	}
	ca, err := provisioner.MySQLServerCA(ctx, docker, container)
	if err != nil {
		t.Fatalf("server CA of %s: %v", container, err)
	}
	tlsConfig, err := mysqltls.ClientConfig(ca, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = provisioner.MySQLUserAdmin, password, "shop"
	cfg.Net, cfg.Addr, cfg.TLS = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.HostPorts["3306"])), tlsConfig
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	return sql.OpenDB(connector)
}
