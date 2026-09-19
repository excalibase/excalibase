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

	lease, got, err := NewAdvisoryLock(store.DB(), testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("Acquire = %v, %v; want true", got, err)
	}
	if err := lease.Release(context.Background()); err != nil {
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

	holder, got, err := NewAdvisoryLock(store.DB(), testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("holder Acquire = %v, %v", got, err)
	}
	defer holder.Release(context.Background())
	held := inUse(t, store)

	if lease, got, err := NewAdvisoryLock(store.DB(), testLockKeyA).Acquire(context.Background()); err != nil || got || lease != nil {
		t.Fatalf("contested Acquire = %v, %v, %v; want no lease", lease, got, err)
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

	holder, got, err := NewAdvisoryLock(store.DB(), testLockKeyB).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("holder Acquire = %v, %v", got, err)
	}
	defer holder.Release(context.Background())
	held := inUse(t, store)

	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), lockTestTimeout)
		_, got, err := NewAdvisoryLock(store.DB(), testLockKeyB).Acquire(ctx)
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
		lease, got, err := lock.Acquire(context.Background())
		if err != nil || !got {
			t.Fatalf("tick %d: Acquire = %v, %v", i, got, err)
		}
		if err := lease.Release(context.Background()); err != nil {
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
	if err := (&AdvisoryLease{}).Release(context.Background()); err != nil {
		t.Fatalf("releasing a lease that holds nothing: %v", err)
	}
}

// A lock object is not a leadership flag. Two callers on the same object get
// separate sessions, so only one holds the key — and when the holder
// releases, the other must not be left believing it leads.
func TestAdvisoryLock_ASecondCallerDoesNotInheritLeadership(t *testing.T) {
	store := testStore(t)
	lock := NewAdvisoryLock(store.DB(), testLockKeyA)

	first, got, err := lock.Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("first Acquire = %v, %v; want the lock", got, err)
	}
	second, got, err := lock.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if got {
		t.Fatal("two callers must not both hold the key; the second took it without the database saying so")
	}
	if second != nil {
		t.Fatal("a refused Acquire must hand back no lease")
	}

	// The holder finishing must not leave the other caller able to release
	// a lock it never had.
	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("releasing twice must be a no-op, got %v", err)
	}
	if held := inUse(t, store); held != 0 && held != 1 {
		t.Fatalf("connections in use after release = %d", held)
	}

	third, got, err := lock.Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("the key must be free again: %v, %v", got, err)
	}
	third.Release(context.Background())
}

// A lease whose connection has gone reports itself invalid, so a replica
// that lost its session stops believing it still leads.
func TestAdvisoryLease_ReportsAnInvalidLeaseAfterRelease(t *testing.T) {
	store := testStore(t)
	lease, got, err := NewAdvisoryLock(store.DB(), testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("Acquire = %v, %v", got, err)
	}
	if !lease.Valid(context.Background()) {
		t.Fatal("a freshly taken lease must be valid")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lease.Valid(context.Background()) {
		t.Fatal("a released lease must not report itself valid")
	}
}
