package service

import (
	"context"
	"hash/fnv"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/storage"
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

// advisoryDeletionClaimer holds the claim as a Postgres advisory lock, so it
// is visible to every control-plane replica sharing the platform database and
// is released automatically if the holder's connection dies. Unlike the
// schedulers' standing leadership, a deletion claim is per call: it covers
// exactly one teardown run.
type advisoryDeletionClaimer struct {
	newLock func(key int64) storage.LeaderLock
}

// NewAdvisoryDeletionClaimer builds a claimer over per-project advisory locks.
// newLock returns a lock for one int64 key; the key is derived from the
// project id.
func NewAdvisoryDeletionClaimer(newLock func(key int64) storage.LeaderLock) DeletionClaimer {
	return &advisoryDeletionClaimer{newLock: newLock}
}

func (c *advisoryDeletionClaimer) Claim(ctx context.Context, projectID string) (func(), bool, error) {
	// One lease per claim: the teardown that took it is the only thing that
	// can give it back, and a claim that lost holds nothing to give back.
	lease, got, err := c.newLock(deletionLockKey(projectID)).Acquire(ctx)
	if err != nil || !got {
		return nil, false, err
	}
	return func() { _ = lease.Release(context.WithoutCancel(ctx)) }, true, nil
}

// deletionLockKey maps a project id onto the advisory-lock key space. The
// high bit is cleared so the key is always a positive int64; the platform's
// two singleton scheduler keys are fixed constants in main.go, and a hash
// collision with either is no more likely than with any other key.
func deletionLockKey(projectID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("project-deletion:" + projectID))
	return int64(h.Sum64() &^ (1 << 63))
}
