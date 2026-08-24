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
//
// pgdogTestEnv bundles the wired collaborators a pgdog notifier subtest needs.
type pgdogTestEnv struct {
	store       *pgstore.Store
	notifier    *PgDogNotifier
	natsURL     string
	reloadCount *int32
}

// setupPgDogNotifierEnv starts Postgres + NATS, creates the pgdog tables,
// subscribes to the reload subject, and builds the notifier under test. All
// resources are registered for cleanup on the test.
func setupPgDogNotifierEnv(ctx context.Context, t *testing.T) pgdogTestEnv {
	t.Helper()
	store := startPgDogPostgres(ctx, t)
	natsURL := startNATS(ctx, t)
	reloadCount := subscribeReload(t, natsURL)

	n, err := NewPgDogNotifier(store, natsURL)
	if err != nil {
		t.Fatalf("NewPgDogNotifier: %v", err)
	}
	t.Cleanup(n.Close)
	return pgdogTestEnv{store: store, notifier: n, natsURL: natsURL, reloadCount: reloadCount}
}

// startPgDogPostgres boots Postgres and creates the pgdog_databases /
// pgdog_users tables (PgDog owns them in production; mirrored here for a
// hermetic test). Returns the connected store.
func startPgDogPostgres(ctx context.Context, t *testing.T) *pgstore.Store {
	t.Helper()
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
	return store
}

// startNATS boots a NATS container and returns its client URL.
func startNATS(ctx context.Context, t *testing.T) string {
	t.Helper()
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
	return fmt.Sprintf("nats://%s:%s", natsHost, natsPort.Port())
}

// subscribeReload subscribes to pgdog.config.reload before the notifier
// publishes (so we don't race) and returns a counter incremented per message.
func subscribeReload(t *testing.T, natsURL string) *int32 {
	t.Helper()
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
	if err := subConn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return &reloadCount
}

// waitForReload polls the reload counter until it reaches at least 1 within 2s.
func waitForReload(reloadCount *int32) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(reloadCount) >= 1 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestPgDogNotifier_Integration(t *testing.T) { //NOSONAR sequential integration scenario; splitting would obscure the flow
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	env := setupPgDogNotifierEnv(ctx, t)
	store := env.store
	n := env.notifier
	reloadCount := env.reloadCount

	t.Run("RegisterCluster_writes_DB_rows", func(t *testing.T) {
		atomic.StoreInt32(reloadCount, 0)
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
		if !waitForReload(reloadCount) {
			t.Errorf("expected ≥1 reload signal; got %d", atomic.LoadInt32(reloadCount))
		}
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
		atomic.StoreInt32(reloadCount, 0)
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
		if !waitForReload(reloadCount) {
			t.Errorf("deregister should publish reload signal; count=%d", atomic.LoadInt32(reloadCount))
		}
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
