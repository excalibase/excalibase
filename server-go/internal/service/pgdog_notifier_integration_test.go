//go:build integration

package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	tcgeneric "github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// TestPgDogNotifier_Integration exercises the full register → NATS
// publish → deregister flow against real Postgres + NATS containers.
// Replaces the old shell-based tests/pgdog/test-nats-reload.sh that
// required a live K8s cluster + PgDog binary; this version is
// self-contained and can run in CI.
//
// What gets verified:
//
//   - RegisterCluster writes 2 rows to pgdog_databases (primary +
//     replica) and 1 row to pgdog_users with the expected hosts +
//     read-only flag
//   - Calling Register with the same projectId twice is upsert-safe
//     (no UNIQUE violations)
//   - RegisterCluster publishes a "reload" message on the
//     pgdog.config.reload subject — subscriber receives within 2s
//   - DeregisterCluster removes the rows AND publishes another
//     reload signal
//   - Notifier with no NATS URL is silent (no panic on publish, no
//     dependency on NATS being up)
func TestPgDogNotifier_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// --- Postgres for the pgdog_databases / pgdog_users tables ----------
	pgPwd := testutil.FixturePassword("pgdog-int")
	pgC, err := tcpg.Run(ctx, "postgres:16-alpine",
		tcpg.WithDatabase("platform"),
		tcpg.WithUsername("platform"),
		tcpg.WithPassword(pgPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })

	pgHost, _ := pgC.Host(ctx)
	pgPort, _ := pgC.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://platform:%s@%s:%s/platform?sslmode=disable", pgPwd, pgHost, pgPort.Port())
	store, err := pgstore.New(dsn)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// PgDog's tables aren't created by our migrations — PgDog owns
	// them in production. Mirror the schema inline so this test is
	// hermetic. Same SQL as coverage_gap_test.go's pgdogTablesSQL.
	if _, err := store.DB().Exec(`
		CREATE TABLE IF NOT EXISTS pgdog_databases (
		    name TEXT NOT NULL,
		    host TEXT NOT NULL,
		    port INTEGER NOT NULL,
		    database_name TEXT NOT NULL,
		    role TEXT NOT NULL,
		    shard INTEGER NOT NULL DEFAULT 0,
		    pool_size INTEGER,
		    read_only BOOLEAN NOT NULL DEFAULT FALSE,
		    active BOOLEAN NOT NULL DEFAULT TRUE,
		    PRIMARY KEY (name, role, shard)
		);
		CREATE TABLE IF NOT EXISTS pgdog_users (
		    name TEXT NOT NULL,
		    database TEXT NOT NULL,
		    password TEXT,
		    pool_size INTEGER,
		    active BOOLEAN NOT NULL DEFAULT TRUE,
		    PRIMARY KEY (name, database)
		);`); err != nil {
		t.Fatalf("create pgdog tables: %v", err)
	}

	// --- NATS ------------------------------------------------------------
	natsC, err := tcgeneric.GenericContainer(ctx, tcgeneric.GenericContainerRequest{
		ContainerRequest: tcgeneric.ContainerRequest{
			Image:        "nats:2.10-alpine",
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	t.Cleanup(func() { natsC.Terminate(ctx) })
	natsHost, _ := natsC.Host(ctx)
	natsPort, _ := natsC.MappedPort(ctx, "4222/tcp")
	natsURL := fmt.Sprintf("nats://%s:%s", natsHost, natsPort.Port())

	// Subscribe BEFORE the notifier publishes so we don't race.
	subConn, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("subscriber connect: %v", err)
	}
	t.Cleanup(subConn.Close)
	var reloadCount int32
	sub, err := subConn.Subscribe("pgdog.config.reload", func(_ *nats.Msg) {
		atomic.AddInt32(&reloadCount, 1)
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { sub.Unsubscribe() })
	// Flush so the subscription is registered server-side before we publish.
	if err := subConn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// --- Notifier under test -------------------------------------------
	n, err := NewPgDogNotifier(store, natsURL)
	if err != nil {
		t.Fatalf("NewPgDogNotifier: %v", err)
	}
	t.Cleanup(n.Close)

	t.Run("RegisterCluster_writes_DB_rows", func(t *testing.T) {
		atomic.StoreInt32(&reloadCount, 0)
		if err := n.RegisterCluster(ctx, "proj-alpha", "ns-alpha", "app", "app", "secret-alpha"); err != nil {
			t.Fatalf("RegisterCluster: %v", err)
		}
		// Database rows.
		var primaryHost, replicaHost string
		var replicaRO bool
		row := store.DB().QueryRow(`
			SELECT host FROM pgdog_databases
			WHERE name = $1 AND role = 'primary'`, "proj-alpha")
		if err := row.Scan(&primaryHost); err != nil {
			t.Fatalf("query primary: %v", err)
		}
		if primaryHost != "proj-alpha-postgres-rw.ns-alpha.svc.cluster.local" {
			t.Errorf("primary host: %q", primaryHost)
		}
		row = store.DB().QueryRow(`
			SELECT host, read_only FROM pgdog_databases
			WHERE name = $1 AND role = 'replica'`, "proj-alpha")
		if err := row.Scan(&replicaHost, &replicaRO); err != nil {
			t.Fatalf("query replica: %v", err)
		}
		if !replicaRO {
			t.Errorf("replica should be read_only=true")
		}
		// User row.
		var userPass string
		row = store.DB().QueryRow(`SELECT password FROM pgdog_users WHERE name = $1 AND database = $2`, "app", "proj-alpha")
		if err := row.Scan(&userPass); err != nil {
			t.Fatalf("query user: %v", err)
		}
		if userPass != "secret-alpha" {
			t.Errorf("user password not stored")
		}
	})

	t.Run("RegisterCluster_publishes_reload_to_NATS", func(t *testing.T) {
		// Allow a short window for the message to land.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if atomic.LoadInt32(&reloadCount) >= 1 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("expected ≥1 reload signal; got %d", atomic.LoadInt32(&reloadCount))
	})

	t.Run("RegisterCluster_idempotent_upsert", func(t *testing.T) {
		// Second register with same projectId — must not 23505 the
		// pgdog_databases UNIQUE constraint or duplicate rows.
		if err := n.RegisterCluster(ctx, "proj-alpha", "ns-alpha", "app", "app", "rotated-secret"); err != nil {
			t.Fatalf("re-register: %v", err)
		}
		var rows int
		row := store.DB().QueryRow(`SELECT COUNT(*) FROM pgdog_databases WHERE name = $1`, "proj-alpha")
		if err := row.Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 2 {
			t.Errorf("expected 2 rows after upsert (primary + replica), got %d", rows)
		}
		var pw string
		store.DB().QueryRow(`SELECT password FROM pgdog_users WHERE database = 'proj-alpha'`).Scan(&pw)
		if pw != "rotated-secret" {
			t.Errorf("password rotation didn't take: %q", pw)
		}
	})

	t.Run("DeregisterCluster_removes_rows_and_signals", func(t *testing.T) {
		atomic.StoreInt32(&reloadCount, 0)
		if err := n.DeregisterCluster(ctx, "proj-alpha", "app"); err != nil {
			t.Fatalf("Deregister: %v", err)
		}
		var dbRows, userRows int
		store.DB().QueryRow(`SELECT COUNT(*) FROM pgdog_databases WHERE name = 'proj-alpha'`).Scan(&dbRows)
		store.DB().QueryRow(`SELECT COUNT(*) FROM pgdog_users WHERE database = 'proj-alpha'`).Scan(&userRows)
		if dbRows != 0 {
			t.Errorf("expected 0 db rows after deregister, got %d", dbRows)
		}
		if userRows != 0 {
			t.Errorf("expected 0 user rows after deregister, got %d", userRows)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if atomic.LoadInt32(&reloadCount) >= 1 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("deregister should publish reload signal; count=%d", atomic.LoadInt32(&reloadCount))
	})

	t.Run("NoNATS_silent_register", func(t *testing.T) {
		// Notifier with empty NATS URL still writes DB rows; just
		// doesn't publish. This is the self-hosted / dev path.
		nNoNATS, err := NewPgDogNotifier(store, "")
		if err != nil {
			t.Fatalf("NewPgDogNotifier no-nats: %v", err)
		}
		t.Cleanup(nNoNATS.Close)
		if err := nNoNATS.RegisterCluster(ctx, "proj-beta", "ns-beta", "app", "app", "secret-beta"); err != nil {
			t.Fatalf("Register no-nats: %v", err)
		}
		var rows int
		store.DB().QueryRow(`SELECT COUNT(*) FROM pgdog_databases WHERE name = 'proj-beta'`).Scan(&rows)
		if rows != 2 {
			t.Errorf("rows persisted even without NATS: got %d, want 2", rows)
		}
	})
}
