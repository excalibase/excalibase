package service

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// silentDatabase accepts connections and never answers, as a database pod
// does once a restore or failover has frozen or replaced it.
func silentDatabase(t *testing.T) (host string, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
}

// answersWithin fails the test if call has not returned an error by limit.
func answersWithin(t *testing.T, limit time.Duration, call func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent database answered")
		}
	case <-time.After(limit):
		t.Fatalf("the call hung for %s on a database that does not answer", limit)
	}
}

func silentOverrides(t *testing.T) dsnOverrides {
	host, port := silentDatabase(t)
	return dsnOverrides{host: host, port: strconv.Itoa(port), sslmode: "disable"}
}

func appRecord() map[string]string {
	return map[string]string{"host": "db.internal", "port": "5432", "username": "excalibase_app",
		"password": "pw", "database": "app"}
}

// EXC-650: lib/pq has no read timeout and a cancelled context does not
// unblock its login, so each one-off connection must bound itself.
func TestRestoreProbeFailsWhenTheDatabaseDoesNotAnswer(t *testing.T) {
	vault := newFakeVault()
	vault.data["projects/proj_a/credentials/excalibase_app"] = appRecord()
	probe := &VaultDatabaseProbe{vault: vault, overrides: silentOverrides(t), timeout: defaultProbeTimeout}

	answersWithin(t, 30*time.Second, func() error { return probe.Probe(context.Background(), "proj_a") })
}

func TestRotatedCredentialCheckFailsWhenTheDatabaseDoesNotAnswer(t *testing.T) {
	verifier := &TenantRoleVerifier{overrides: silentOverrides(t), timeout: defaultProbeTimeout}
	inst := &domain.DatabaseInstance{ProjectID: "proj_a", Host: "db.internal", DatabaseName: "app"}

	answersWithin(t, 30*time.Second, func() error {
		return verifier.VerifyRole(context.Background(), inst, "app_user", "pw", "")
	})
}

func TestMigrationFailsWhenTheDatabaseDoesNotAnswer(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	vault.data["projects/proj_a/credentials/excalibase_app"] = appRecord()
	migrations := NewMigrationService(store, vault, t.TempDir())
	migrations.overrides = silentOverrides(t)

	answersWithin(t, 30*time.Second, func() error {
		record, err := migrations.ApplyMigration(context.Background(), "proj_a",
			domain.MigrationRequest{Version: "1", Name: "init", SQL: "SELECT 1"})
		if err == nil && record.Status == "FAILED" {
			return errFailedMigration(record.ErrorMessage)
		}
		return err
	})
}

type errFailedMigration string

func (e errFailedMigration) Error() string { return "migration failed: " + string(e) }
