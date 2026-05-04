package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// AdvisoryLock backs storage.LeaderLock by Postgres pg_try_advisory_lock.
// The lock is session-scoped — released either by Release or by the
// connection closing. Cloud-mode platform replicas all attempt to
// acquire the same key; only one wins per tick of the scheduler.
type AdvisoryLock struct {
	db  *sql.DB
	key int64
	// conn pins a single connection so unlock targets the same
	// session that held the lock. Without pinning, the second call
	// could race against connection reuse.
	conn *sql.Conn
}

// NewAdvisoryLock builds a lock for the given int64 key. Pick a
// project-wide constant key in the application; e.g. the FNV-1a
// hash of "backup-scheduler".
func NewAdvisoryLock(db *sql.DB, key int64) *AdvisoryLock {
	return &AdvisoryLock{db: db, key: key}
}

// Acquire returns true if this caller now holds the lock; false if
// another replica holds it. Tries pg_try_advisory_lock — never blocks.
func (l *AdvisoryLock) Acquire(ctx context.Context) (bool, error) {
	if l.conn == nil {
		c, err := l.db.Conn(ctx)
		if err != nil {
			return false, fmt.Errorf("acquire conn: %w", err)
		}
		l.conn = c
	}
	var got bool
	if err := l.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil {
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	return got, nil
}

// Release runs pg_advisory_unlock against the same session that
// holds the lock. Safe to call when the lock isn't held — Postgres
// returns false in that case but doesn't error.
func (l *AdvisoryLock) Release(ctx context.Context) error {
	if l.conn == nil {
		return nil
	}
	if _, err := l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		return fmt.Errorf("pg_advisory_unlock: %w", err)
	}
	return nil
}
