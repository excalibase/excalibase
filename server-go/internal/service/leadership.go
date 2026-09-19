package service

import (
	"context"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Leadership is a replica's standing claim to run the platform's schedules.
//
// The lock answers "which replica fires the schedules", not "which job runs
// now": the cron runs every due project's job in its own goroutine, and all
// of them belong to the replica that leads. So leadership is taken once and
// held across ticks rather than acquired per job — a per-job acquire would
// hand the key to whichever job asked first and skip every other project due
// in the same minute.
//
// The claim ends when the process does: the lock is session-scoped, so a
// replica that dies releases it and another takes over on its next tick.
type Leadership struct {
	lock storage.LeaderLock

	mu    sync.Mutex
	lease storage.LeaderLease
}

// NewLeadership wraps a leader lock as a standing claim.
func NewLeadership(lock storage.LeaderLock) *Leadership {
	return &Leadership{lock: lock}
}

// IsLeader reports whether this replica may fire schedules now. It takes the
// claim the first time, then answers from the lease it holds — re-checking
// that the lease is still live, so a lost session stands down instead of
// firing alongside the replica that took over.
func (l *Leadership) IsLeader(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lease != nil {
		if l.lease.Valid(ctx) {
			return true, nil
		}
		// The session is gone; so is the claim it stood for.
		_ = l.lease.Release(ctx)
		l.lease = nil
	}

	lease, acquired, err := l.lock.Acquire(ctx)
	if err != nil || !acquired {
		return false, err
	}
	l.lease = lease
	return true, nil
}

// Close stands down, releasing the claim so another replica can take it
// without waiting for this process's session to be reaped.
func (l *Leadership) Close(ctx context.Context) error {
	l.mu.Lock()
	lease := l.lease
	l.lease = nil
	l.mu.Unlock()
	if lease == nil {
		return nil
	}
	return lease.Release(ctx)
}

// AlwaysLeader is a no-op LeaderLock for single-process self-hosted
// deployments where there's only ever one platform replica.
type AlwaysLeader struct{}

func (AlwaysLeader) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return noopLease{}, true, nil
}

type noopLease struct{}

func (noopLease) Release(context.Context) error { return nil }
func (noopLease) Valid(context.Context) bool    { return true }
