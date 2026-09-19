package service

import (
	"context"
	"errors"
	"testing"
)

func TestInProcessClaimerGrantsOneHolderAtATime(t *testing.T) {
	claimer := newInProcessDeletionClaimer()
	release, ok, err := claimer.Claim(context.Background(), "proj-1")
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if _, ok, _ := claimer.Claim(context.Background(), "proj-1"); ok {
		t.Error("a second claim on the same project must be refused")
	}
	if _, ok, _ := claimer.Claim(context.Background(), "proj-2"); !ok {
		t.Error("another project must still be claimable")
	}
	release()
	if _, ok, _ := claimer.Claim(context.Background(), "proj-1"); !ok {
		t.Error("the project must be claimable again once released")
	}
}

// fakeLock stands in for the Postgres advisory lock.
type fakeLock struct {
	got       bool
	acquired  int
	released  int
	acquerErr error
}

func (l *fakeLock) Acquire(context.Context) (bool, error) { l.acquired++; return l.got, l.acquerErr }
func (l *fakeLock) Release(context.Context) error         { l.released++; return nil }

func TestAdvisoryClaimerHoldsAndReleasesTheLock(t *testing.T) {
	lock := &fakeLock{got: true}
	claimer := NewAdvisoryDeletionClaimer(func(int64) AdvisoryLocker { return lock })

	release, ok, err := claimer.Claim(context.Background(), "proj-1")
	if err != nil || !ok {
		t.Fatalf("Claim = %v, %v", ok, err)
	}
	release()
	if lock.acquired != 1 || lock.released != 1 {
		t.Errorf("acquired=%d released=%d, want 1/1", lock.acquired, lock.released)
	}
}

func TestAdvisoryClaimerReportsARefusedLock(t *testing.T) {
	claimer := NewAdvisoryDeletionClaimer(func(int64) AdvisoryLocker { return &fakeLock{} })
	if _, ok, err := claimer.Claim(context.Background(), "proj-1"); ok || err != nil {
		t.Fatalf("Claim = %v, %v; want refused without error", ok, err)
	}
}

func TestAdvisoryClaimerReportsLockErrors(t *testing.T) {
	want := errors.New("database unreachable")
	claimer := NewAdvisoryDeletionClaimer(func(int64) AdvisoryLocker {
		return &fakeLock{acquerErr: want}
	})
	if _, ok, err := claimer.Claim(context.Background(), "proj-1"); ok || !errors.Is(err, want) {
		t.Fatalf("Claim = %v, %v; want the lock error", ok, err)
	}
}

// The key is stable per project and distinct between projects, and always
// positive so it cannot collide with the platform's singleton locks.
func TestDeletionLockKeyIsStableAndPositive(t *testing.T) {
	first := deletionLockKey("proj-1")
	if first != deletionLockKey("proj-1") {
		t.Error("the key must be stable for a project")
	}
	if first == deletionLockKey("proj-2") {
		t.Error("different projects must not share a key")
	}
	if first < 0 {
		t.Errorf("key = %d, want a positive key", first)
	}
}
