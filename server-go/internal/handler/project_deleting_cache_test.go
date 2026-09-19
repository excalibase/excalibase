package handler

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// countingInstances records how often the invoke path reaches the platform
// database, which is the whole point of the cache.
type countingInstances struct {
	storage.InstanceStore
	reads int
}

func (s *countingInstances) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	s.reads++
	return s.InstanceStore.FindByProjectID(projectID)
}

func cacheFixture(t *testing.T, status string) (*projectStatusCache, *countingInstances, *time.Time) {
	t.Helper()
	items := fakestore.NewInstances()
	items.Create(&domain.DatabaseInstance{ProjectID: "proj-1", Status: status})
	counting := &countingInstances{InstanceStore: items}
	now := time.Unix(1_700_000_000, 0)
	cache := newProjectStatusCache()
	cache.now = func() time.Time { return now }
	return cache, counting, &now
}

// Invocation must not read the platform database on every request.
func TestDeletingCacheReadsThroughAtMostOncePerWindow(t *testing.T) {
	cache, instances, _ := cacheFixture(t, "ACTIVE")

	for i := 0; i < 20; i++ {
		if cache.deleting(instances, "proj-1") {
			t.Fatalf("call %d: an active project must not read as deleting", i)
		}
	}
	if instances.reads != 1 {
		t.Fatalf("store reads = %d, want 1 for a whole cache window", instances.reads)
	}
}

// Once the window passes the answer is checked again, so a project deleted
// by another replica stops being served.
func TestDeletingCacheExpires(t *testing.T) {
	cache, instances, now := cacheFixture(t, "ACTIVE")
	cache.deleting(instances, "proj-1")

	*now = now.Add(deletingCacheTTL)
	cache.deleting(instances, "proj-1")
	if instances.reads != 2 {
		t.Fatalf("store reads = %d, want a second read after the window", instances.reads)
	}
}

// The replica that ran the DELETE stops immediately rather than at expiry.
func TestDeletingCacheIsInvalidatedByTheClaim(t *testing.T) {
	cache, instances, _ := cacheFixture(t, "ACTIVE")
	if cache.deleting(instances, "proj-1") {
		t.Fatal("precondition: the project starts live")
	}

	cache.ProjectDeleting("proj-1")
	if !cache.deleting(instances, "proj-1") {
		t.Fatal("a claimed project must stop being served at once")
	}
	if instances.reads != 1 {
		t.Errorf("store reads = %d; the claim should not need another read", instances.reads)
	}
}

// A deleting project is refused, and a project the store does not know is
// left to the caller's own lookup to report.
func TestDeletingCacheAnswers(t *testing.T) {
	cache, instances, _ := cacheFixture(t, string(domain.StatusDeleting))
	if !cache.deleting(instances, "proj-1") {
		t.Error("a deleting project must be refused")
	}
	if cache.deleting(instances, "missing") {
		t.Error("an unknown project is not this check's to refuse")
	}
	if cache.deleting(nil, "proj-1") {
		t.Error("without a store the check must not refuse")
	}
}

// A platform-database blip must not take tenant traffic down with it.
func TestDeletingCacheServesThroughAStoreFailure(t *testing.T) {
	items := fakestore.NewInstances()
	items.Err = errStoreDown
	cache := newProjectStatusCache()
	if cache.deleting(&countingInstances{InstanceStore: items}, "proj-1") {
		t.Error("an unreadable store must not refuse tenant traffic")
	}
}
