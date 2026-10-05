package storage

import (
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Every status the platform writes, and whether a project in it occupies one
// of its organisation's tier project slots. A deleted project's row lingers
// until teardown is observed complete, so counting those states would keep a
// user out of the slot they already gave up — and a stuck teardown would keep
// them out forever.
func TestHoldsOrgProjectSlot_PerStatus(t *testing.T) {
	cases := []struct {
		status string
		holds  bool
	}{
		{domain.StatusProvisioning, true},
		{string(domain.StatusRestoring), true},
		{"ACTIVE", true},
		{string(domain.StatusPausing), true},
		{string(domain.StatusPaused), true},
		{string(domain.StatusResuming), true},
		{string(domain.StageFailed), true},
		{string(domain.StatusPendingDeletion), false},
		{string(domain.StatusDeleting), false},
		{string(domain.StatusBackupsPendingDelete), false},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			if got := HoldsOrgProjectSlot(c.status); got != c.holds {
				t.Fatalf("HoldsOrgProjectSlot(%q) = %v, want %v", c.status, got, c.holds)
			}
		})
	}
}

// The SQL every store filters with must name exactly the statuses the
// predicate excludes, or the Postgres count and the in-memory count disagree.
func TestNonSlotStatuses_MatchThePredicate(t *testing.T) {
	for _, status := range NonSlotStatuses() {
		if HoldsOrgProjectSlot(status) {
			t.Fatalf("%q is filtered out by the store queries but holds a slot", status)
		}
	}
	if len(NonSlotStatuses()) != 3 {
		t.Fatalf("expected the grace period and the two deletion statuses, got %v", NonSlotStatuses())
	}
}

func TestCheckOrgProjectSlot(t *testing.T) {
	cases := []struct {
		name        string
		held, limit int
		wantErr     bool
	}{
		{"unlimited tier", 99, 0, false},
		{"negative limit is unlimited", 99, -1, false},
		{"below the limit", 1, 2, false},
		{"at the limit", 2, 2, true},
		{"over the limit", 3, 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckOrgProjectSlot(c.held, c.limit)
			if c.wantErr != (err != nil) {
				t.Fatalf("CheckOrgProjectSlot(%d, %d) = %v", c.held, c.limit, err)
			}
			if c.wantErr && !errors.Is(err, ErrOrgProjectLimitReached) {
				t.Fatalf("expected ErrOrgProjectLimitReached, got %v", err)
			}
		})
	}
}

func TestFileSystemStore_CreateWithinOrgLimit_RefusesAFullOrg(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", Status: "ACTIVE"})

	err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{ProjectID: "proj-2", OrgID: "org1", Status: "PROVISIONING"}, 1)
	if !errors.Is(err, ErrOrgProjectLimitReached) {
		t.Fatalf("expected ErrOrgProjectLimitReached, got %v", err)
	}
	if inst, _ := store.FindByProjectID("proj-2"); inst != nil {
		t.Fatal("the refused project must not have been written")
	}
}

// The defect this ticket exists for: a project deleted a moment ago still has
// a row, and must not keep its organisation at the limit.
func TestFileSystemStore_CreateWithinOrgLimit_IgnoresDeletingRows(t *testing.T) {
	for _, status := range NonSlotStatuses() {
		t.Run(status, func(t *testing.T) {
			store := newTempFileStore(t)
			mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: status})

			if err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{
				ProjectID: "proj-new", OrgID: "org1", Status: "PROVISIONING",
			}, 1); err != nil {
				t.Fatalf("a project under teardown must not hold a slot: %v", err)
			}
		})
	}
}

func TestFileSystemStore_CreateWithinOrgLimit_CountsOnlyTheOwningOrg(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-other", OrgID: "org2", Status: "ACTIVE"})

	if err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{
		ProjectID: "proj-mine", OrgID: "org1", Status: "PROVISIONING",
	}, 1); err != nil {
		t.Fatalf("another org's project must not count: %v", err)
	}
}

func TestFileSystemStore_CreateWithinOrgLimit_UnlimitedTier(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", Status: "ACTIVE"})

	if err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{
		ProjectID: "proj-2", OrgID: "org1", Status: "PROVISIONING",
	}, 0); err != nil {
		t.Fatalf("an unlimited tier must admit the project: %v", err)
	}
}

func TestFileSystemStore_CreateWithinOrgLimit_RefusesATakenID(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", Status: "ACTIVE"})

	err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", Status: "PROVISIONING"}, 5)
	if !errors.Is(err, ErrProjectExists) {
		t.Fatalf("expected ErrProjectExists, got %v", err)
	}
}

func TestFileSystemStore_CountOrgProjects(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", Status: "ACTIVE"})
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-2", OrgID: "org1", Status: string(domain.StatusDeleting)})
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-3", OrgID: "org2", Status: "ACTIVE"})

	count, err := store.CountOrgProjects("org1")
	if err != nil {
		t.Fatalf("CountOrgProjects: %v", err)
	}
	if count != 1 {
		t.Fatalf("count: got %d, want 1", count)
	}
}

// A project in its 7-day grace period gave its slot up when it was deleted, so
// restoring it takes a slot again: refused while the org is full, and the row
// keeps its grace period.
func TestFileSystemStore_UpdateIfStatusWithinOrgLimit_RestoreRefusedAtTheLimit(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-new", OrgID: "org1", Status: "ACTIVE"})
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPendingDeletion)})

	restored := &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPaused)}
	err := store.UpdateIfStatusWithinOrgLimit(restored, string(domain.StatusPendingDeletion), 1)
	if !errors.Is(err, ErrOrgProjectLimitReached) {
		t.Fatalf("expected ErrOrgProjectLimitReached, got %v", err)
	}
	if inst, _ := store.FindByProjectID("proj-old"); inst.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("a refused restore must leave the row in its grace period, got %s", inst.Status)
	}
}

func TestFileSystemStore_UpdateIfStatusWithinOrgLimit_RestoreTakesAFreeSlot(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPendingDeletion)})
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-other", OrgID: "org2", Status: "ACTIVE"})

	restored := &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPaused)}
	if err := store.UpdateIfStatusWithinOrgLimit(restored, string(domain.StatusPendingDeletion), 1); err != nil {
		t.Fatalf("restore with a free slot: %v", err)
	}
	if count, _ := store.CountOrgProjects("org1"); count != 1 {
		t.Fatalf("the restored project must hold the slot again, count %d", count)
	}
	if err := store.CreateWithinOrgLimit(&domain.DatabaseInstance{ProjectID: "proj-new", OrgID: "org1", Status: "PROVISIONING"}, 1); !errors.Is(err, ErrOrgProjectLimitReached) {
		t.Fatalf("a create after the restore must find the org full, got %v", err)
	}
}

func TestFileSystemStore_UpdateIfStatusWithinOrgLimit_RefusesAChangedStatus(t *testing.T) {
	store := newTempFileStore(t)
	mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPaused)})

	restored := &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPaused)}
	err := store.UpdateIfStatusWithinOrgLimit(restored, string(domain.StatusPendingDeletion), 5)
	if !errors.Is(err, ErrProjectStatusChanged) {
		t.Fatalf("expected ErrProjectStatusChanged, got %v", err)
	}
}

// A restore and a create racing for the last slot: exactly one wins.
func TestFileSystemStore_RestoreAndCreateRaceForTheLastSlot(t *testing.T) {
	for round := 0; round < 20; round++ {
		store := newTempFileStore(t)
		mustCreate(t, store, &domain.DatabaseInstance{ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPendingDeletion)})

		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(2)
		var restoreErr, createErr error
		go func() {
			defer done.Done()
			start.Wait()
			restoreErr = store.UpdateIfStatusWithinOrgLimit(&domain.DatabaseInstance{
				ProjectID: "proj-old", OrgID: "org1", Status: string(domain.StatusPaused),
			}, string(domain.StatusPendingDeletion), 1)
		}()
		go func() {
			defer done.Done()
			start.Wait()
			createErr = store.CreateWithinOrgLimit(&domain.DatabaseInstance{ProjectID: "proj-new", OrgID: "org1", Status: "PROVISIONING"}, 1)
		}()
		start.Done()
		done.Wait()

		if (restoreErr == nil) == (createErr == nil) {
			t.Fatalf("round %d: restore %v, create %v; exactly one must win", round, restoreErr, createErr)
		}
		if count, _ := store.CountOrgProjects("org1"); count != 1 {
			t.Fatalf("round %d: org holds %d slots, want 1", round, count)
		}
	}
}

func newTempFileStore(t *testing.T) *FileSystemStore {
	t.Helper()
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSystemStore: %v", err)
	}
	return store
}

func mustCreate(t *testing.T, store *FileSystemStore, inst *domain.DatabaseInstance) {
	t.Helper()
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create %s: %v", inst.ProjectID, err)
	}
}
