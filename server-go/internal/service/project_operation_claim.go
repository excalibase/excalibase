package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ProjectOperation names the lifecycle operation holding a project's lease.
// It appears in the log and in what a refused caller is told; it is NOT part
// of the lease key, because the whole point is that these operations exclude
// one another.
type ProjectOperation string

const (
	OperationDeletion ProjectOperation = "deletion"
	OperationPause    ProjectOperation = "pause"
	OperationResume   ProjectOperation = "resume"
	OperationRotation ProjectOperation = "rotation"
)

// ErrProjectOperationRunning is what every caller refused the project's
// lifecycle lease is told. It names no operation on purpose: for an advisory
// lease the holder's identity is not knowable, and guessing produced the
// worst kind of message — a DELETE blocked by a pause was told to wait for a
// deletion that did not exist. It wraps storage.ErrProjectBusy so the gate
// and the handlers keep answering 409 for it.
var ErrProjectOperationRunning = fmt.Errorf(
	"%w: another operation on this project is running; retry when it finishes", storage.ErrProjectBusy)

// ProjectOperationClaimer grants one mutating lifecycle operation at a time
// per project, across every control-plane replica.
//
// Without it these operations interleave on a last-writer-wins row: replica A
// stops a project's pods, and before it can record PAUSED replica B is asked
// to resume, sees PAUSING over a stopped workload, starts the database, and
// A then writes PAUSED over something that is running. The lease is what
// makes "one operation at a time" true rather than hoped for.
//
// Claim reports whether this caller now holds the project. The returned
// release must be called when the run ends, on every path.
type ProjectOperationClaimer interface {
	Claim(ctx context.Context, projectID string, op ProjectOperation) (release func(), claimed bool, err error)
}

// inProcessOperationClaimer serialises lifecycle operations inside one
// control-plane process. It is the default; deployments running several
// replicas against one platform database wire the advisory-lock claimer so
// the lease holds across them.
type inProcessOperationClaimer struct {
	mu      sync.Mutex
	running map[string]ProjectOperation
}

// NewInProcessOperationClaimer serialises lifecycle operations inside one
// process. Correct for a single replica and for stores with no advisory
// locks; a multi-replica control plane wires the advisory-lock claimer.
func NewInProcessOperationClaimer() ProjectOperationClaimer {
	return newInProcessOperationClaimer()
}

func newInProcessOperationClaimer() *inProcessOperationClaimer {
	return &inProcessOperationClaimer{running: map[string]ProjectOperation{}}
}

func (c *inProcessOperationClaimer) Claim(_ context.Context, projectID string, op ProjectOperation) (func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.running[projectID]; busy {
		return nil, false, nil
	}
	c.running[projectID] = op
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.running, projectID)
	}, true, nil
}

// advisoryOperationClaimer holds the lease as a Postgres advisory lock, so it
// is visible to every replica sharing the platform database and is released
// automatically if the holder's connection dies. Unlike the schedulers'
// standing leadership, this lease is per call: it covers exactly one run.
type advisoryOperationClaimer struct {
	newLock func(key int64) storage.LeaderLock
}

// NewAdvisoryOperationClaimer builds a claimer over per-project advisory
// locks. newLock returns a lock for one int64 key, derived from the project.
func NewAdvisoryOperationClaimer(newLock func(key int64) storage.LeaderLock) ProjectOperationClaimer {
	return &advisoryOperationClaimer{newLock: newLock}
}

func (c *advisoryOperationClaimer) Claim(ctx context.Context, projectID string, _ ProjectOperation) (func(), bool, error) {
	// One lease per claim: the run that took it is the only thing that can
	// give it back, and a run that lost holds nothing to give back.
	lease, got, err := c.newLock(projectLifecycleLockKey(projectID)).Acquire(ctx)
	if err != nil || !got {
		return nil, false, err
	}
	return func() { _ = lease.Release(context.WithoutCancel(ctx)) }, true, nil
}

// lifecycleLockPrefix is effectively a wire format: replicas only exclude one
// another while they agree on it, so changing it would leave a rolling deploy
// with two populations taking different keys for the same project. It keeps
// the name deletion first used for that reason.
const lifecycleLockPrefix = "project-deletion:"

// projectLifecycleLockKey maps a project id onto the advisory-lock key space.
// One key per PROJECT, not per operation — pause, resume and deletion must
// exclude each other, which they cannot do from separate keys. The high bit
// is cleared so the key is always a positive int64.
func projectLifecycleLockKey(projectID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(lifecycleLockPrefix + projectID))
	return int64(h.Sum64() &^ (1 << 63))
}
