//go:build integration

package byoc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startPostgres(t *testing.T) (host, port string) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("byoc_test"),
		postgres.WithUsername("byoc"),
		postgres.WithPassword("byoc"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, err = c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	return host, mapped.Port()
}

// The DSN names a public hostname; the guard resolves it (fake resolver),
// classifies the answer and dials the classified address. The injected dialer
// redirects that one address to the container, so a successful query proves
// lib/pq really went through Guard.DialTimeout rather than its own resolver.
func TestGuard_OpenDB_ConnectsThroughGuardedDialer(t *testing.T) {
	host, port := startPostgres(t)
	resolver := newFakeResolver(map[string][]string{"db.example": {"203.0.113.10"}})
	g := NewGuard(Policy{}, resolver)
	rec := &recordingDialer{}
	g.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		rec.mu.Lock()
		rec.addrs = append(rec.addrs, address)
		rec.mu.Unlock()
		if address != "203.0.113.10:5432" {
			return nil, fmt.Errorf("unexpected dial target %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
	}

	db, err := g.OpenDB("host=db.example port=5432 user=byoc password=byoc dbname=byoc_test sslmode=disable connect_timeout=5")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var one int
	if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query through guarded dialer: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d", one)
	}
	if got := rec.dialed(); len(got) != 1 || got[0] != "203.0.113.10:5432" {
		t.Errorf("dialed %v, want exactly the classified address", got)
	}
	if n := resolver.lookups("db.example"); n != 1 {
		t.Errorf("guarded dial must resolve exactly once, got %d", n)
	}
}

// A live Postgres on loopback is unreachable through the guard even though
// a plain sql.Open reaches it: the refusal is the guard's, not the network's.
func TestGuard_OpenDB_RefusesLoopbackEvenWhenReachable(t *testing.T) {
	host, port := startPostgres(t)
	if host != "localhost" && host != "127.0.0.1" {
		t.Skipf("container host %q is not loopback; refusal would not isolate the guard", host)
	}
	dsn := fmt.Sprintf("host=127.0.0.1 port=%s user=byoc password=byoc dbname=byoc_test sslmode=disable connect_timeout=5", port)

	plain, err := NewGuard(Policy{}, nil).OpenProjectDB(nil, "managed", dsn)
	if err != nil {
		t.Fatalf("plain open: %v", err)
	}
	defer plain.Close()
	if err := plain.PingContext(context.Background()); err != nil {
		t.Fatalf("control: plain dial to the container must work: %v", err)
	}

	guarded, err := NewGuard(Policy{}, nil).OpenDB(dsn)
	if err != nil {
		t.Fatalf("guarded open: %v", err)
	}
	defer guarded.Close()
	if err := guarded.PingContext(context.Background()); !errors.Is(err, ErrInternalAddress) {
		t.Fatalf("guarded ping = %v, want ErrInternalAddress", err)
	}
}
