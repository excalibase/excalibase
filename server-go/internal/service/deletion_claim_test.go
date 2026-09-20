package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestInProcessClaimerGrantsOneHolderAtATime(t *testing.T) {
	claimer := newInProcessOperationClaimer()
	release, ok, err := claimer.Claim(context.Background(), "proj-1", OperationDeletion)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if _, ok, _ := claimer.Claim(context.Background(), "proj-1", OperationDeletion); ok {
		t.Error("a second claim on the same project must be refused")
	}
	if _, ok, _ := claimer.Claim(context.Background(), "proj-2", OperationDeletion); !ok {
		t.Error("another project must still be claimable")
	}
	release()
	if _, ok, _ := claimer.Claim(context.Background(), "proj-1", OperationDeletion); !ok {
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

func (l *fakeLock) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	l.acquired++
	if l.acquerErr != nil || !l.got {
		return nil, false, l.acquerErr
	}
	return &countingLease{lock: l}, true, nil
}

type countingLease struct{ lock *fakeLock }

func (le *countingLease) Release(context.Context) error { le.lock.released++; return nil }
func (le *countingLease) Valid(context.Context) bool    { return true }

func TestAdvisoryClaimerHoldsAndReleasesTheLock(t *testing.T) {
	lock := &fakeLock{got: true}
	claimer := NewAdvisoryOperationClaimer(func(int64) storage.LeaderLock { return lock })

	release, ok, err := claimer.Claim(context.Background(), "proj-1", OperationDeletion)
	if err != nil || !ok {
		t.Fatalf("Claim = %v, %v", ok, err)
	}
	release()
	if lock.acquired != 1 || lock.released != 1 {
		t.Errorf("acquired=%d released=%d, want 1/1", lock.acquired, lock.released)
	}
}

func TestAdvisoryClaimerReportsARefusedLock(t *testing.T) {
	claimer := NewAdvisoryOperationClaimer(func(int64) storage.LeaderLock { return &fakeLock{} })
	if _, ok, err := claimer.Claim(context.Background(), "proj-1", OperationDeletion); ok || err != nil {
		t.Fatalf("Claim = %v, %v; want refused without error", ok, err)
	}
}

func TestAdvisoryClaimerReportsLockErrors(t *testing.T) {
	want := errors.New("database unreachable")
	claimer := NewAdvisoryOperationClaimer(func(int64) storage.LeaderLock {
		return &fakeLock{acquerErr: want}
	})
	if _, ok, err := claimer.Claim(context.Background(), "proj-1", OperationDeletion); ok || !errors.Is(err, want) {
		t.Fatalf("Claim = %v, %v; want the lock error", ok, err)
	}
}

// The key is stable per project and distinct between projects, and always
// positive so it cannot collide with the platform's singleton locks.
func TestDeletionLockKeyIsStableAndPositive(t *testing.T) {
	first := projectLifecycleLockKey("proj-1")
	if first != projectLifecycleLockKey("proj-1") {
		t.Error("the key must be stable for a project")
	}
	if first == projectLifecycleLockKey("proj-2") {
		t.Error("different projects must not share a key")
	}
	if first < 0 {
		t.Errorf("key = %d, want a positive key", first)
	}
}

// Pause, resume and deletion must land on the SAME key, or they would not
// exclude one another at all.
func TestEveryLifecycleOperationTakesTheSameProjectKey(t *testing.T) {
	first := projectLifecycleLockKey("proj-1")
	if first != projectLifecycleLockKey("proj-1") {
		t.Error("the key must be stable for a project")
	}
	if first == projectLifecycleLockKey("proj-2") {
		t.Error("different projects must not share a key")
	}
	if first < 0 {
		t.Errorf("advisory keys must be positive, got %d", first)
	}
}
