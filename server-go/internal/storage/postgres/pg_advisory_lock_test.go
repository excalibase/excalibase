//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"
)

// lockTestTimeout bounds each contested claim so a pool that has run out of
// connections shows up as a timeout rather than a hung test.
const lockTestTimeout = 5 * time.Second

const (
	testLockKeyA int64 = 0x0102_0304_0506_0701
	testLockKeyB int64 = 0x0102_0304_0506_0702
)

// inUse waits for the pool to settle and reports the connections still held.
func inUse(t *testing.T, store *Store) int {
	t.Helper()
	return store.DB().Stats().InUse
}

// A lock that has been released must hold nothing: the session-scoped lock
// needs its own backend while held, but keeping that backend afterwards takes
// a connection out of the pool for good.
func TestAdvisoryLock_ReleaseReturnsTheConnection(t *testing.T) {
	store := testStore(t)
	baseline := inUse(t, store)

	lock := NewAdvisoryLock(store.DB(), testLockKeyA)
	got, err := lock.Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("Acquire = %v, %v; want true", got, err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if held := inUse(t, store); held != baseline {
		t.Fatalf("connections in use after release = %d, want the baseline %d", held, baseline)
	}
}

// A contested claim never gets the lock, so nothing will ever call Release on
// it — it must not be holding a connection in the first place.
func TestAdvisoryLock_ContestedClaimHoldsNoConnection(t *testing.T) {
	store := testStore(t)
	baseline := inUse(t, store)

	holder := NewAdvisoryLock(store.DB(), testLockKeyA)
	if got, err := holder.Acquire(context.Background()); err != nil || !got {
		t.Fatalf("holder Acquire = %v, %v", got, err)
	}
	defer holder.Release(context.Background())
	held := inUse(t, store)

	loser := NewAdvisoryLock(store.DB(), testLockKeyA)
	if got, err := loser.Acquire(context.Background()); err != nil || got {
		t.Fatalf("contested Acquire = %v, %v; want false", got, err)
	}
	if now := inUse(t, store); now != held {
		t.Fatalf("connections in use after a lost claim = %d, want %d", now, held)
	}
	if held != baseline+1 {
		t.Fatalf("a held lock should pin exactly one connection: held=%d baseline=%d", held, baseline)
	}
}

// The deletion claim builds a lock per request, and a DELETE retried while
// the first run holds the lock is the expected case. Repeating it must not
// drain the pool — with five connections available, fifty lost claims would
// otherwise deadlock on the sixth.
func TestAdvisoryLock_RepeatedContestedClaimsDoNotExhaustThePool(t *testing.T) {
	store := testStore(t)
	store.DB().SetMaxOpenConns(5)

	holder := NewAdvisoryLock(store.DB(), testLockKeyB)
	if got, err := holder.Acquire(context.Background()); err != nil || !got {
		t.Fatalf("holder Acquire = %v, %v", got, err)
	}
	defer holder.Release(context.Background())
	held := inUse(t, store)

	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), lockTestTimeout)
		loser := NewAdvisoryLock(store.DB(), testLockKeyB)
		got, err := loser.Acquire(ctx)
		cancel()
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if got {
			t.Fatalf("claim %d took a lock another session holds", i)
		}
	}
	if now := inUse(t, store); now != held {
		t.Fatalf("connections in use after 50 lost claims = %d, want %d", now, held)
	}
}

// A released lock is reusable: the schedulers acquire and release the same
// lock object on every tick.
func TestAdvisoryLock_IsReusableAfterRelease(t *testing.T) {
	store := testStore(t)
	baseline := inUse(t, store)
	lock := NewAdvisoryLock(store.DB(), testLockKeyA)

	for i := 0; i < 3; i++ {
		got, err := lock.Acquire(context.Background())
		if err != nil || !got {
			t.Fatalf("tick %d: Acquire = %v, %v", i, got, err)
		}
		if err := lock.Release(context.Background()); err != nil {
			t.Fatalf("tick %d: Release: %v", i, err)
		}
	}
	if held := inUse(t, store); held != baseline {
		t.Fatalf("connections in use after the last release = %d, want the baseline %d", held, baseline)
	}
}

// Releasing a lock that was never acquired is a no-op, not an error: the
// schedulers defer Release on paths that may not have taken it.
func TestAdvisoryLock_ReleaseWithoutAcquireIsSafe(t *testing.T) {
	store := testStore(t)
	if err := NewAdvisoryLock(store.DB(), testLockKeyA).Release(context.Background()); err != nil {
		t.Fatalf("Release without Acquire: %v", err)
	}
}
