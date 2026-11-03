//go:build integration

package projectdb

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// stallingProxy forwards to a database until stall: from then on every open
// connection stays up but nothing crosses it, as when the pod behind a
// pooled connection is replaced. After resume only new connections forward.
type stallingProxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	stalled  bool
	links    []*atomic.Bool
	conns    []net.Conn
}

func startStallingProxy(t *testing.T, target string) *stallingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &stallingProxy{listener: listener, target: target}
	go proxy.serve()
	t.Cleanup(proxy.close)
	return proxy
}

func (p *stallingProxy) port() string {
	return strconv.Itoa(p.listener.Addr().(*net.TCPAddr).Port)
}

func (p *stallingProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		dead := &atomic.Bool{}
		p.mu.Lock()
		dead.Store(p.stalled)
		p.links = append(p.links, dead)
		p.conns = append(p.conns, client)
		p.mu.Unlock()
		if dead.Load() {
			go discard(client)
			continue
		}
		upstream, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, upstream)
		p.mu.Unlock()
		go pump(upstream, client, dead)
		go pump(client, upstream, dead)
	}
}

func pump(dst, src net.Conn, dead *atomic.Bool) {
	defer func() { _ = dst.Close(); _ = src.Close() }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !dead.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func discard(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4<<10)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

func (p *stallingProxy) stall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stalled = true
	for _, dead := range p.links {
		dead.Store(true)
	}
}

func (p *stallingProxy) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stalled = false
}

func (p *stallingProxy) close() {
	_ = p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

// selectOneWithin runs SELECT 1, failing the test if it outlives limit.
func selectOneWithin(t *testing.T, db *sql.DB, limit time.Duration) (time.Duration, error) {
	t.Helper()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		var one int
		done <- db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)
	}()
	select {
	case err := <-done:
		return time.Since(started), err
	case <-time.After(limit):
		t.Fatalf("the call hung for %s on a server that stopped answering", limit)
		return 0, nil
	}
}

// failsFastThenReconnects warms db, stalls the server under it, and wants
// the next call to fail within failBy and the one after a resume to reach
// the server again.
func failsFastThenReconnects(t *testing.T, proxy *stallingProxy, db *sql.DB, failBy time.Duration) {
	t.Helper()
	if _, err := selectOneWithin(t, db, 60*time.Second); err != nil {
		t.Fatalf("warm the pool: %v", err)
	}
	proxy.stall()
	elapsed, err := selectOneWithin(t, db, 90*time.Second)
	if err == nil {
		t.Fatal("a stalled server answered")
	}
	if elapsed > failBy {
		t.Fatalf("the call failed only after %s", elapsed)
	}
	proxy.resume()
	elapsed, err = selectOneWithin(t, db, 90*time.Second)
	if err != nil {
		t.Fatalf("the next call did not reach the server: %v", err)
	}
	if elapsed > failBy {
		t.Fatalf("the next call took %s; the stale connection was not dropped", elapsed)
	}
}

func startPlainPostgres(t *testing.T) (host, port string) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("excalibase_app"), postgres.WithPassword("app-pw"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err = container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return host, mapped.Port()
}

// EXC-650: after an in-place restore or failover a pooled connection to the
// replaced server must fail the call within the pool's read bound, not hold
// it forever, and the next call must reach the new server.
func TestIntegration_APostgresProjectPoolFailsFastWhenTheServerStopsAnswering(t *testing.T) {
	host, port := startPlainPostgres(t)
	proxy := startStallingProxy(t, net.JoinHostPort(host, port))
	vault := fakeVault{data: map[string]map[string]string{
		"projects/proj_pg/credentials/excalibase_app": {"host": "db.internal", "port": "5432",
			"username": "excalibase_app", "password": "app-pw", "database": "app"},
	}}
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_pg", Status: "ACTIVE"}), vault,
		Overrides{Host: "127.0.0.1", Port: proxy.port(), SSLMode: "disable"}, PoolLimits{StatementTimeout: time.Second})
	t.Cleanup(o.Close)
	db, err := o.Open(context.Background(), "proj_pg")
	if err != nil {
		t.Fatal(err)
	}
	failsFastThenReconnects(t, proxy, db, 20*time.Second)
}

func TestIntegration_AOneOffPostgresConnectionFailsFastWhenTheServerStopsAnswering(t *testing.T) {
	host, port := startPlainPostgres(t)
	proxy := startStallingProxy(t, net.JoinHostPort(host, port))
	dsn, err := DSNFor(map[string]string{"host": "db.internal", "port": "5432", "username": "excalibase_app",
		"password": "app-pw", "database": "app"}, Overrides{Host: "127.0.0.1", Port: proxy.port(), SSLMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenPostgres(dsn, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	failsFastThenReconnects(t, proxy, db, 12*time.Second)
}

func TestIntegration_AMySQLProjectPoolFailsFastWhenTheServerStopsAnswering(t *testing.T) {
	server := startProvisionedMySQL(t)
	proxy := startStallingProxy(t, net.JoinHostPort(server.host, server.port))
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		server.vault, Overrides{Host: "127.0.0.1", Port: proxy.port(), SSLMode: "verify-full"},
		PoolLimits{StatementTimeout: time.Second})
	t.Cleanup(o.Close)
	for _, script := range []bool{false, true} {
		db, err := o.OpenMySQL(context.Background(), "proj_my", script)
		if err != nil {
			t.Fatal(err)
		}
		failsFastThenReconnects(t, proxy, db, 20*time.Second)
	}
}
