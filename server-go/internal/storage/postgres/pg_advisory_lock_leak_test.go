//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"
)

// advisoryLocksHeld counts session advisory locks held by any backend. A
// lock parked on a pooled connection shows here even though re-taking it on
// the same session would succeed (the lock is re-entrant).
func advisoryLocksHeld(t *testing.T, store *Store) int {
	t.Helper()
	var held int
	if err := store.DB().QueryRow(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}

// Callers cancel mid-flight (client left, deadline passed). Whatever state the
// lock ended in, the pool must get every connection back and the key must be
// free again, or the next caller is told "busy" forever.
func TestAdvisoryLock_CancelledCallersLeakNeitherConnectionsNorLocks(t *testing.T) {
	store := testStore(t)
	store.DB().SetMaxOpenConns(3)
	baseline := inUse(t, store)
	lock := NewAdvisoryLock(store.DB(), testLockKeyA)

	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+(i+worker)%5)*time.Millisecond)
				lease, got, _ := lock.Acquire(ctx)
				if got {
					lease.Valid(ctx)
					_ = lease.Release(ctx) // possibly already expired
				}
				cancel()
			}
		}(worker)
	}
	wg.Wait()

	deadline := time.Now().Add(lockTestTimeout)
	for store.DB().Stats().InUse != baseline && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if held := store.DB().Stats().InUse; held != baseline {
		t.Fatalf("connections in use after every caller finished = %d, want the baseline %d", held, baseline)
	}
	if held := advisoryLocksHeld(t, store); held != 0 {
		t.Fatalf("%d advisory locks are parked on pooled connections nobody owns", held)
	}
}

// A release that cannot run its unlock (caller's context already done) must
// not hand the connection back to the pool still holding the session lock.
func TestAdvisoryLock_ReleaseWithDeadContextStillFreesTheKey(t *testing.T) {
	store := testStore(t)
	lock := NewAdvisoryLock(store.DB(), testLockKeyB)
	lease, got, err := lock.Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("Acquire = %v, %v", got, err)
	}
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	_ = lease.Release(dead)

	if held := advisoryLocksHeld(t, store); held != 0 {
		t.Fatalf("the key stays locked on a pooled connection (%d advisory locks held)", held)
	}
}

// A freshly opened store must have every connection back in the pool. The
// schema migrator used to keep one pinned for the life of the process, so the
// pool every request shares was always one short.
func TestNewStore_HoldsNoConnectionAfterMigrating(t *testing.T) {
	store := testStore(t)
	if held := store.DB().Stats().InUse; held != 0 {
		t.Fatalf("connections in use right after opening the store = %d, want 0", held)
	}
}
