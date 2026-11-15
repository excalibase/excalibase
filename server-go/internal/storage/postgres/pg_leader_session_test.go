//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"
)

// Every scheduler holds a standing leadership claim for as long as it leads.
// With one connection per claim, twenty-odd claims drained the platform
// pool and every request behind it hung (EXC-746). Standing claims must share
// one session, whatever their number.
func TestLeaderSession_ManyStandingClaimsPinOneConnection(t *testing.T) {
	store := testStore(t)
	store.DB().SetMaxOpenConns(3)
	session := NewLeaderSession(store.DB())

	const claims = 25
	for i := 0; i < claims; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), lockTestTimeout)
		_, got, err := session.Lock(testLockKeyA + int64(i)).Acquire(ctx)
		cancel()
		if err != nil || !got {
			t.Fatalf("claim %d: got=%v err=%v", i, got, err)
		}
	}
	if held := store.DB().Stats().InUse; held != 1 {
		t.Fatalf("%d standing claims pin %d connections, want 1", claims, held)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockTestTimeout)
	defer cancel()
	var one int
	if err := store.DB().QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("a request cannot get a connection while claims are held: %v", err)
	}
}

// Two callers on one process must not both lead: the database lock is
// re-entrant within a session, so the session has to say no itself.
func TestLeaderSession_SameKeyIsHeldOnce(t *testing.T) {
	store := testStore(t)
	session := NewLeaderSession(store.DB())
	first, got, err := session.Lock(testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("first: %v %v", got, err)
	}
	if lease, got, err := session.Lock(testLockKeyA).Acquire(context.Background()); err != nil || got || lease != nil {
		t.Fatalf("second claim on a held key = %v, %v, %v; want refusal", lease, got, err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if held := advisoryLocksHeld(t, store); held != 0 {
		t.Fatalf("%d advisory locks remain after release", held)
	}
	again, got, err := session.Lock(testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("key must be free again: %v %v", got, err)
	}
	_ = again.Release(context.Background())
	if inUse(t, store) != 0 {
		t.Fatalf("the session must give its connection back when no claim is held; in use = %d", inUse(t, store))
	}
}

// A session whose backend dies loses every lock with it: each lease reports
// itself invalid so its owner stands down, and a fresh claim works.
func TestLeaderSession_LostBackendInvalidatesLeasesAndRecovers(t *testing.T) {
	store := testStore(t)
	session := NewLeaderSession(store.DB())
	lease, got, err := session.Lock(testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("Acquire: %v %v", got, err)
	}
	if !lease.Valid(context.Background()) {
		t.Fatal("fresh lease must be valid")
	}
	if _, err := store.DB().Exec(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype = 'advisory' AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if lease.Valid(context.Background()) {
		t.Fatal("a lease whose session died must report invalid")
	}
	_ = lease.Release(context.Background())
	next, got, err := session.Lock(testLockKeyA).Acquire(context.Background())
	if err != nil || !got {
		t.Fatalf("claim after losing the session: %v %v", got, err)
	}
	_ = next.Release(context.Background())
}

func TestLeaderSession_ContestedKeyIsRefusedAndFreesTheConnection(t *testing.T) {
	store := testStore(t)
	holder, got, err := NewAdvisoryLock(store.DB(), testLockKeyB).Acquire(context.Background())
	if err != nil || !got {
		t.Fatal(got, err)
	}
	defer holder.Release(context.Background())
	held := inUse(t, store)
	session := NewLeaderSession(store.DB())
	if lease, got, err := session.Lock(testLockKeyB).Acquire(context.Background()); err != nil || got || lease != nil {
		t.Fatalf("contested = %v %v %v", lease, got, err)
	}
	if now := inUse(t, store); now != held {
		t.Fatalf("a refused claim holds a connection: %d vs %d", now, held)
	}
}
