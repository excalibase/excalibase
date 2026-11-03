package projectdb

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/lib/pq"
)

// ConnectTimeout bounds dial, TLS and login on a project database.
const ConnectTimeout = 10 * time.Second

// ProbeIOTimeout bounds each read and write of a short one-off check: a
// liveness probe, a credential proof, a publication toggle.
const ProbeIOTimeout = 10 * time.Second

// MigrationIOTimeout bounds each read and write of a migration. A migration
// runs with no statement timeout and an index build on a large table can
// take many minutes, so this only ends the wait on a server that is gone.
const MigrationIOTimeout = 30 * time.Minute

// poolIOMargin is how long a pooled read outlives the session's
// statement_timeout, so a slow statement ends with the server's own error
// rather than a dropped connection.
const poolIOMargin = 10 * time.Second

// OpenPostgres opens a lib/pq pool whose every read and write is bounded by
// ioTimeout. lib/pq has no read timeout and a cancelled context does not
// unblock a read, so without this a server that stops answering (its pod
// replaced by a restore or failover) holds the call forever (EXC-650).
func OpenPostgres(dsn string, ioTimeout time.Duration) (*sql.DB, error) {
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		// lib/pq can echo the connection string, password included.
		return nil, errors.New("project database connection string is not valid")
	}
	connector.Dialer(boundedDialer{timeout: ioTimeout})
	return sql.OpenDB(connector), nil
}

// boundedDialer wraps every connection it opens in a deadlineConn.
type boundedDialer struct {
	timeout time.Duration
}

func (d boundedDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d boundedDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, network, address)
}

func (d boundedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &deadlineConn{Conn: conn, timeout: d.timeout}, nil
}

// deadlineConn arms a fresh deadline before each read and write. A deadline
// the driver sets itself (its connect_timeout) wins while it is sooner.
type deadlineConn struct {
	net.Conn
	timeout time.Duration

	mu         sync.Mutex
	readLimit  time.Time
	writeLimit time.Time
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	if err := c.arm(&c.readLimit, c.Conn.SetReadDeadline); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	if err := c.arm(&c.writeLimit, c.Conn.SetWriteDeadline); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func (c *deadlineConn) arm(limit *time.Time, set func(time.Time) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.Now().Add(c.timeout)
	if !limit.IsZero() && limit.Before(deadline) {
		deadline = *limit
	}
	return set(deadline)
}

func (c *deadlineConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLimit, c.writeLimit = t, t
	return c.Conn.SetDeadline(t)
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLimit = t
	return c.Conn.SetReadDeadline(t)
}

func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeLimit = t
	return c.Conn.SetWriteDeadline(t)
}

// ioTimeout bounds each read and write on a pooled connection.
func (l PoolLimits) ioTimeout() time.Duration {
	return l.StatementTimeout + poolIOMargin
}
