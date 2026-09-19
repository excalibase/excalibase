package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// AdvisoryLock backs storage.LeaderLock by Postgres pg_try_advisory_lock.
// The lock is session-scoped — released either by Release or by the
// connection closing. Cloud-mode platform replicas all attempt to
// acquire the same key; only one wins per tick of the scheduler.
type AdvisoryLock struct {
	db  *sql.DB
	key int64

	mu sync.Mutex
	// conn pins a single connection so unlock targets the same session that
	// took the lock. Without pinning, the second call could race against
	// connection reuse. It is held only while the lock is held: every path
	// that does not end up holding the lock returns the connection to the
	// pool, or a caller that keeps losing a contested lock would drain it.
	conn *sql.Conn
	held bool
}

// NewAdvisoryLock builds a lock for the given int64 key. Pick a
// project-wide constant key in the application; e.g. the FNV-1a
// hash of "backup-scheduler".
func NewAdvisoryLock(db *sql.DB, key int64) *AdvisoryLock {
	return &AdvisoryLock{db: db, key: key}
}

// Acquire returns true if this caller now holds the lock; false if
// another replica holds it. Tries pg_try_advisory_lock — never blocks.
// It is safe to call again on the same lock: a lock already held reports
// true without taking a second connection.
func (l *AdvisoryLock) Acquire(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return true, nil
	}

	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire conn: %w", err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil {
		closeConn(conn)
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !got {
		// Someone else holds it. Nothing to release later, so the
		// connection goes back now rather than being stranded.
		closeConn(conn)
		return false, nil
	}
	l.conn, l.held = conn, true
	return true, nil
}

// Release runs pg_advisory_unlock against the same session that holds the
// lock and then returns that connection to the pool. Safe to call when the
// lock isn't held. A connection that has already died still releases the
// lock — Postgres drops session locks when the backend goes — so an unlock
// error is reported but never leaves the connection behind.
func (l *AdvisoryLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		l.held = false
		return nil
	}

	conn := l.conn
	l.conn, l.held = nil, false
	_, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key)
	closeConn(conn)
	if err != nil {
		return fmt.Errorf("pg_advisory_unlock: %w", err)
	}
	return nil
}

// closeConn returns a pinned connection to the pool, ignoring the error from
// one that is already gone — there is nothing a caller could do about it and
// the connection is accounted for either way.
func closeConn(conn *sql.Conn) {
	_ = conn.Close()
}
