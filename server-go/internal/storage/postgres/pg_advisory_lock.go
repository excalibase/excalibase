package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Verify AdvisoryLock implements the leader-lock contract at compile time.
var _ storage.LeaderLock = (*AdvisoryLock)(nil)

// AdvisoryLock backs storage.LeaderLock by Postgres pg_try_advisory_lock.
// The lock object itself holds nothing: it is a handle on a key, and every
// Acquire that wins hands back its own lease. Leadership therefore cannot be
// inherited — a second caller asking the same lock object gets a separate
// session and is told, by the database, that the key is taken.
type AdvisoryLock struct {
	db  *sql.DB
	key int64
}

// NewAdvisoryLock builds a lock for the given int64 key. Pick a
// project-wide constant key in the application; e.g. the FNV-1a
// hash of "backup-scheduler".
func NewAdvisoryLock(db *sql.DB, key int64) *AdvisoryLock {
	return &AdvisoryLock{db: db, key: key}
}

// Acquire returns a lease when this caller now holds the lock, and
// (nil, false, nil) when another session holds it. Tries
// pg_try_advisory_lock — never blocks. Every path that does not end up
// holding the key returns its connection to the pool, so a caller that keeps
// losing a contested lock cannot drain it.
func (l *AdvisoryLock) Acquire(ctx context.Context) (storage.LeaderLease, bool, error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire conn: %w", err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil {
		closeConn(conn)
		return nil, false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !got {
		closeConn(conn)
		return nil, false, nil
	}
	return &AdvisoryLease{key: l.key, conn: conn}, true, nil
}

// AdvisoryLease is one caller's hold on an advisory lock. It owns the
// session that took the lock, so only its holder can release it and
// releasing twice does nothing.
type AdvisoryLease struct {
	key int64

	mu   sync.Mutex
	conn *sql.Conn
}

// Release unlocks the key and returns the pinned connection to the pool.
// Safe to call more than once. A connection that has already died still
// released the lock — Postgres drops session locks when the backend goes —
// so an unlock error is reported but never leaves the connection behind.
func (l *AdvisoryLease) Release(ctx context.Context) error {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn == nil {
		return nil
	}

	_, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key)
	closeConn(conn)
	if err != nil {
		return fmt.Errorf("pg_advisory_unlock: %w", err)
	}
	return nil
}

// Valid reports whether the lease still holds its session, and so still
// holds the key. A replica whose connection died must stop believing it
// leads.
func (l *AdvisoryLease) Valid(ctx context.Context) bool {
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()
	if conn == nil {
		return false
	}
	return conn.PingContext(ctx) == nil
}

// closeConn returns a pinned connection to the pool, ignoring the error from
// one that is already gone — there is nothing a caller could do about it and
// the connection is accounted for either way.
func closeConn(conn *sql.Conn) {
	_ = conn.Close()
}
