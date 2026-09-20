//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// A lifecycle lease pins one platform-database connection for the length of
// its operation, so every path out has to give it back: a pause runs for
// minutes, and one that leaked its connection would drain the pool the rest
// of the control plane shares.
func TestLifecycleLeaseReturnsItsConnection(t *testing.T) {
	ctx := context.Background()
	store := startPgDogPostgres(ctx, t)
	claimer := NewAdvisoryOperationClaimer(
		func(key int64) storage.LeaderLock { return pgstore.NewAdvisoryLock(store.DB(), key) })
	baseline := store.DB().Stats().InUse

	release, ok, err := claimer.Claim(ctx, "proj-lease", OperationPause)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if got := store.DB().Stats().InUse; got != baseline+1 {
		t.Errorf("a held lease pins exactly one connection: got %d, want %d", got, baseline+1)
	}
	release()
	if got := store.DB().Stats().InUse; got != baseline {
		t.Errorf("connections in use after release: got %d, want the baseline %d", got, baseline)
	}
}

// Pause and resume take the same key, so the second is refused while the
// first holds it — and a claim that loses must hold no connection, however
// many times it loses.
func TestASecondLifecycleOperationIsRefusedAndHoldsNothing(t *testing.T) {
	ctx := context.Background()
	store := startPgDogPostgres(ctx, t)
	claimer := NewAdvisoryOperationClaimer(
		func(key int64) storage.LeaderLock { return pgstore.NewAdvisoryLock(store.DB(), key) })

	release, ok, err := claimer.Claim(ctx, "proj-busy", OperationPause)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	defer release()
	held := store.DB().Stats().InUse

	for _, op := range []ProjectOperation{OperationResume, OperationDeletion, OperationPause} {
		if _, got, err := claimer.Claim(ctx, "proj-busy", op); got || err != nil {
			t.Fatalf("%s must be refused while the project is held: got=%v err=%v", op, got, err)
		}
	}
	if got := store.DB().Stats().InUse; got != held {
		t.Errorf("losing claims must hold nothing: got %d, want %d", got, held)
	}

	// A different project is unaffected.
	other, ok, err := claimer.Claim(ctx, "proj-other", OperationPause)
	if err != nil || !ok {
		t.Fatalf("a different project must be claimable: ok=%v err=%v", ok, err)
	}
	other()
}
