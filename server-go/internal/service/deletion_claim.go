package service

import (
	"context"
	"hash/fnv"
	"sync"
)

// DeletionClaimer grants one teardown at a time per project. The DELETE is
// synchronous and can outlive an edge proxy's timeout, so the client may
// retry while the first run is still working; the claim is what stops the two
// from tearing the same project down in parallel.
//
// Claim reports whether this caller now holds the project. The returned
// release must be called when the run ends.
type DeletionClaimer interface {
	Claim(ctx context.Context, projectID string) (release func(), claimed bool, err error)
}

// inProcessDeletionClaimer serialises teardowns inside one control-plane
// process. It is the default; deployments running several replicas against
// one platform database wire the Postgres advisory-lock claimer instead so
// the claim holds across them.
type inProcessDeletionClaimer struct {
	mu      sync.Mutex
	running map[string]bool
}

func newInProcessDeletionClaimer() *inProcessDeletionClaimer {
	return &inProcessDeletionClaimer{running: map[string]bool{}}
}

func (c *inProcessDeletionClaimer) Claim(_ context.Context, projectID string) (func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[projectID] {
		return nil, false, nil
	}
	c.running[projectID] = true
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.running, projectID)
	}, true, nil
}

// AdvisoryLocker is the Postgres advisory lock the store already exposes for
// leader election. Deletion reuses it, keyed per project.
type AdvisoryLocker interface {
	Acquire(ctx context.Context) (bool, error)
	Release(ctx context.Context) error
}

// advisoryDeletionClaimer holds the claim as a Postgres advisory lock, so it
// is visible to every control-plane replica sharing the platform database and
// is released automatically if the holder's connection dies.
type advisoryDeletionClaimer struct {
	newLock func(key int64) AdvisoryLocker
}

// NewAdvisoryDeletionClaimer builds a claimer over per-project advisory locks.
// newLock returns a lock for one int64 key; the key is derived from the
// project id.
func NewAdvisoryDeletionClaimer(newLock func(key int64) AdvisoryLocker) DeletionClaimer {
	return &advisoryDeletionClaimer{newLock: newLock}
}

func (c *advisoryDeletionClaimer) Claim(ctx context.Context, projectID string) (func(), bool, error) {
	lock := c.newLock(deletionLockKey(projectID))
	got, err := lock.Acquire(ctx)
	if err != nil || !got {
		return nil, false, err
	}
	return func() { _ = lock.Release(context.WithoutCancel(ctx)) }, true, nil
}

// deletionLockKey maps a project id onto the advisory-lock key space. The
// high bit is cleared so the key is always positive and never collides with
// the platform's negative-keyed singleton locks.
func deletionLockKey(projectID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("project-deletion:" + projectID))
	return int64(h.Sum64() &^ (1 << 63))
}
