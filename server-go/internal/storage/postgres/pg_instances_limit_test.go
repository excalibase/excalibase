//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestCreateWithinOrgLimit_RefusesAFullOrg(t *testing.T) {
	store := testStore(t)
	if err := store.Create(instanceRow("proj-full0001", "org-full")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := store.CreateWithinOrgLimit(instanceRow("proj-full0002", "org-full"), 1)
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("CreateWithinOrgLimit = %v, want ErrOrgProjectLimitReached", err)
	}
	got, err := store.FindByProjectID("proj-full0002")
	if err != nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got != nil {
		t.Fatal("a refused create must leave no row behind")
	}
}

// A project the user deleted a moment ago keeps its row until teardown is
// observed complete. It must not keep the user out of the slot they gave up,
// and a teardown that never finishes must not hold that slot forever.
func TestCreateWithinOrgLimit_DeletionStatusesFreeTheSlot(t *testing.T) {
	for _, status := range storage.NonSlotStatuses() {
		t.Run(status, func(t *testing.T) {
			store := testStore(t)
			old := instanceRow("proj-gone0001", "org-gone")
			old.Status = status
			if err := store.Create(old); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if err := store.CreateWithinOrgLimit(instanceRow("proj-gone0002", "org-gone"), 1); err != nil {
				t.Fatalf("a project under teardown must not hold a slot: %v", err)
			}
		})
	}
}

func TestCreateWithinOrgLimit_UnlimitedTierAndOtherOrgs(t *testing.T) {
	store := testStore(t)
	if err := store.Create(instanceRow("proj-unl00001", "org-unl")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Create(instanceRow("proj-other001", "org-other")); err != nil {
		t.Fatalf("seed other org: %v", err)
	}

	if err := store.CreateWithinOrgLimit(instanceRow("proj-unl00002", "org-unl"), 0); err != nil {
		t.Fatalf("an unlimited tier must admit the project: %v", err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-mine0001", "org-mine"), 1); err != nil {
		t.Fatalf("another org's projects must not count: %v", err)
	}
}

func TestCreateWithinOrgLimit_RefusesATakenID(t *testing.T) {
	store := testStore(t)
	if err := store.Create(instanceRow("proj-taken001", "org-taken")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := store.CreateWithinOrgLimit(instanceRow("proj-taken001", "org-taken"), 5)
	if !errors.Is(err, storage.ErrProjectExists) {
		t.Fatalf("CreateWithinOrgLimit = %v, want ErrProjectExists", err)
	}
}

func TestCountOrgProjects_CountsOnlySlotHolders(t *testing.T) {
	store := testStore(t)
	live := instanceRow("proj-count001", "org-count")
	if err := store.Create(live); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deleting := instanceRow("proj-count002", "org-count")
	deleting.Status = string(domain.StatusDeleting)
	if err := store.Create(deleting); err != nil {
		t.Fatalf("seed deleting: %v", err)
	}
	if err := store.Create(instanceRow("proj-count003", "org-elsewhere")); err != nil {
		t.Fatalf("seed other org: %v", err)
	}

	count, err := store.CountOrgProjects("org-count")
	if err != nil {
		t.Fatalf("CountOrgProjects: %v", err)
	}
	if count != 1 {
		t.Fatalf("count: got %d, want 1", count)
	}
}

// The reason the limit is enforced in the store rather than read-then-write in
// the service: at READ COMMITTED each transaction would count without seeing
// the other's uncommitted insert, and both would take the last slot. Every
// goroutine starts at the same barrier and asks for the one remaining slot;
// exactly one may get it.
func TestCreateWithinOrgLimit_ConcurrentCreatesTakeOneSlot(t *testing.T) {
	store := testStore(t)
	const limit = 3
	for round := 0; round < 10; round++ {
		org := fmt.Sprintf("org-race%d", round)
		for i := 1; i < limit; i++ {
			if err := store.Create(instanceRow(fmt.Sprintf("proj-r%d-seed%d", round, i), org)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}

		const racers = 8
		var start sync.WaitGroup
		var done sync.WaitGroup
		start.Add(1)
		results := make([]error, racers)
		for i := 0; i < racers; i++ {
			done.Add(1)
			go func(i int) {
				defer done.Done()
				start.Wait()
				results[i] = store.CreateWithinOrgLimit(instanceRow(fmt.Sprintf("proj-r%d-racer%d", round, i), org), limit)
			}(i)
		}
		start.Done()
		done.Wait()

		admitted, refused := 0, 0
		for i, err := range results {
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, storage.ErrOrgProjectLimitReached):
				refused++
			default:
				t.Fatalf("racer %d: unexpected error %v", i, err)
			}
		}
		if admitted != 1 || refused != racers-1 {
			t.Fatalf("admitted %d, refused %d; want 1 and %d", admitted, refused, racers-1)
		}
		count, err := store.CountOrgProjects(org)
		if err != nil {
			t.Fatalf("CountOrgProjects: %v", err)
		}
		if count != limit {
			t.Fatalf("org ended with %d projects, want the limit of %d", count, limit)
		}
	}
}

// A status the update door treats as teardown never holds a slot either; the
// grace period frees the slot without being a teardown status.
func TestDeletionStatuses_HoldNoSlot(t *testing.T) {
	for _, status := range deletionStatuses {
		if storage.HoldsOrgProjectSlot(status) {
			t.Fatalf("%q is a teardown status but holds a slot", status)
		}
	}
}

func pendingDeletionRow(projectID, orgID string) *domain.DatabaseInstance {
	row := instanceRow(projectID, orgID)
	row.Status = string(domain.StatusPendingDeletion)
	return row
}

func restoredRow(projectID, orgID string) *domain.DatabaseInstance {
	row := instanceRow(projectID, orgID)
	row.Status = string(domain.StatusPaused)
	return row
}

func TestUpdateIfStatusWithinOrgLimit_RestoreRefusedAtTheLimit(t *testing.T) {
	store := testStore(t)
	if err := store.Create(pendingDeletionRow("proj-rest0001", "org-rest")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.CreateWithinOrgLimit(instanceRow("proj-rest0002", "org-rest"), 1); err != nil {
		t.Fatalf("a project in its grace period must not hold the slot: %v", err)
	}

	err := store.UpdateIfStatusWithinOrgLimit(restoredRow("proj-rest0001", "org-rest"), string(domain.StatusPendingDeletion), 1)
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("restore = %v, want ErrOrgProjectLimitReached", err)
	}
	got, err := store.FindByProjectID("proj-rest0001")
	if err != nil || got.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("a refused restore must leave the grace period in place: %+v %v", got, err)
	}
}

func TestUpdateIfStatusWithinOrgLimit_RestoreTakesAFreeSlot(t *testing.T) {
	store := testStore(t)
	if err := store.Create(pendingDeletionRow("proj-rest0003", "org-free")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.UpdateIfStatusWithinOrgLimit(restoredRow("proj-rest0003", "org-free"), string(domain.StatusPendingDeletion), 1); err != nil {
		t.Fatalf("restore with a free slot: %v", err)
	}
	count, err := store.CountOrgProjects("org-free")
	if err != nil || count != 1 {
		t.Fatalf("count = %d %v, want the restored project holding the slot", count, err)
	}
	err = store.UpdateIfStatusWithinOrgLimit(restoredRow("proj-rest0003", "org-free"), string(domain.StatusPendingDeletion), 1)
	if !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("a second restore = %v, want ErrProjectStatusChanged", err)
	}
}

// A restore and creates racing for the last slot share the org lock, so
// exactly one of them gets it.
func TestUpdateIfStatusWithinOrgLimit_RestoreAndCreatesRaceForTheLastSlot(t *testing.T) {
	store := testStore(t)
	for round := 0; round < 10; round++ {
		org := fmt.Sprintf("org-rrace%d", round)
		old := fmt.Sprintf("proj-rr%d-old", round)
		if err := store.Create(pendingDeletionRow(old, org)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		const creators = 4
		var start, done sync.WaitGroup
		start.Add(1)
		results := make([]error, creators+1)
		for i := 0; i <= creators; i++ {
			done.Add(1)
			go func(i int) {
				defer done.Done()
				start.Wait()
				if i == creators {
					results[i] = store.UpdateIfStatusWithinOrgLimit(restoredRow(old, org), string(domain.StatusPendingDeletion), 1)
					return
				}
				results[i] = store.CreateWithinOrgLimit(instanceRow(fmt.Sprintf("proj-rr%d-new%d", round, i), org), 1)
			}(i)
		}
		start.Done()
		done.Wait()

		admitted := 0
		for i, err := range results {
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, storage.ErrOrgProjectLimitReached):
			default:
				t.Fatalf("racer %d: unexpected error %v", i, err)
			}
		}
		count, err := store.CountOrgProjects(org)
		if admitted != 1 || err != nil || count != 1 {
			t.Fatalf("round %d: admitted %d, count %d (%v); want one slot taken once", round, admitted, count, err)
		}
	}
}

// A platform database that cannot be reached fails the create and the count,
// rather than answering as if the organisation had room.
func TestOrgProjectLimit_UnreachableDatabaseFails(t *testing.T) {
	store := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := store.CreateWithinOrgLimit(instanceRow("proj-closed001", "org-closed"), 1); err == nil {
		t.Fatal("CreateWithinOrgLimit must fail on a closed database")
	}
	if _, err := store.CountOrgProjects("org-closed"); err == nil {
		t.Fatal("CountOrgProjects must fail on a closed database")
	}
}
