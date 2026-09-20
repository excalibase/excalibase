package storage

import (
	"errors"
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
	if len(NonSlotStatuses()) != 2 {
		t.Fatalf("expected the two deletion statuses, got %v", NonSlotStatuses())
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
