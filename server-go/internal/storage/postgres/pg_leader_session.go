package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// LeaderSession lets every standing leadership claim in the process share one
// database session. A claim is a session-scoped advisory lock, so it needs a
// connection for as long as it is held; one connection per claim let the
// schedulers alone pin more connections than the platform pool owns, after
// which every request waited for a connection that never came (EXC-746).
// Postgres holds any number of different keys on one session, so the claims
// cost one connection in total, taken when the first is won and returned when
// the last is released.
type LeaderSession struct {
	db *sql.DB

	mu   sync.Mutex
	conn *sql.Conn
	gen  uint64
	held map[int64]struct{}
}

// NewLeaderSession builds a session that draws its single connection from db.
func NewLeaderSession(db *sql.DB) *LeaderSession {
	return &LeaderSession{db: db, held: map[int64]struct{}{}}
}

// Lock returns the leader lock for one key, backed by the shared session.
func (s *LeaderSession) Lock(key int64) storage.LeaderLock {
	return &sessionLock{session: s, key: key}
}

// sessionOpTimeout bounds every round trip on the shared session, which every
// leader waits behind.
const sessionOpTimeout = 5 * time.Second

type sessionLock struct {
	session *LeaderSession
	key     int64
}

// Acquire reports (nil, false, nil) when another session, or another claim in
// this process, holds the key. Postgres advisory locks are re-entrant inside
// a session, so the in-process check is what keeps two callers from both
// believing they lead.
func (l *sessionLock) Acquire(ctx context.Context) (storage.LeaderLease, bool, error) {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, mine := s.held[l.key]; mine {
		return nil, false, nil
	}
	if s.conn == nil {
		connCtx, cancel := context.WithTimeout(ctx, acquireConnTimeout)
		defer cancel()
		conn, err := s.db.Conn(connCtx)
		if err != nil {
			return nil, false, fmt.Errorf("%w: acquire conn: %v", storage.ErrPoolBusy, err)
		}
		s.conn = conn
		s.gen++
	}
	// Not derived from the caller's cancellation: a cancelled query ends the
	// session, and with it every other leader's lock. One caller giving up
	// must not take the others down.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionOpTimeout)
	defer cancel()
	var got bool
	if err := s.conn.QueryRowContext(opCtx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil {
		s.dropLocked()
		return nil, false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !got {
		s.returnIfIdleLocked()
		return nil, false, nil
	}
	s.held[l.key] = struct{}{}
	return &sessionLease{session: s, key: l.key, gen: s.gen}, true, nil
}

// dropLocked ends the session. Postgres frees every lock it held, so every
// lease of this generation is dead and says so from Valid.
func (s *LeaderSession) dropLocked() {
	if s.conn != nil {
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = s.conn.Close()
	}
	s.conn = nil
	s.gen++
	s.held = map[int64]struct{}{}
}

// returnIfIdleLocked gives the connection back once no claim needs it.
func (s *LeaderSession) returnIfIdleLocked() {
	if s.conn != nil && len(s.held) == 0 {
		_ = s.conn.Close()
		s.conn = nil
		s.gen++
	}
}

type sessionLease struct {
	session *LeaderSession
	key     int64
	gen     uint64

	released bool // guarded by session.mu
}

// live reports whether the session that took the lock is still the current
// one. The caller holds session.mu.
func (l *sessionLease) live() bool {
	return !l.released && l.session.conn != nil && l.gen == l.session.gen
}

// Valid pings the shared session; a failed ping ends it, so no lease keeps
// believing in a session the database has dropped.
func (l *sessionLease) Valid(ctx context.Context) bool {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !l.live() {
		return false
	}
	pingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionOpTimeout)
	defer cancel()
	if err := s.conn.PingContext(pingCtx); err != nil {
		s.dropLocked()
		return false
	}
	return true
}

// Release unlocks the key. It runs even when the caller's context is done,
// under its own bounded one. Safe to call twice.
func (l *sessionLease) Release(ctx context.Context) error {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	if l.gen != s.gen || s.conn == nil {
		return nil // the session, and the lock with it, is already gone
	}
	delete(s.held, l.key)
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionOpTimeout)
	defer cancel()
	if _, err := s.conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		s.dropLocked() // unknown whether the key was freed; ending the session frees it
		return fmt.Errorf("pg_advisory_unlock: %w", err)
	}
	s.returnIfIdleLocked()
	return nil
}
