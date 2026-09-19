package handler

import (
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// deletingCacheTTL bounds how long the invoke path may act on a stale answer.
// Function invocation is the platform's hottest route and its runtime client
// is cached per project, so without this the deletion check would be the only
// thing making the platform database a per-request dependency of serving
// tenant traffic.
//
// A replica that did not run the DELETE itself keeps serving the project for
// at most this long; the replica that ran it stops immediately, because the
// service invalidates the entry as soon as it claims the project.
const deletingCacheTTL = 5 * time.Second

// projectStatusCache remembers, briefly, whether a project is being deleted.
type projectStatusCache struct {
	mu      sync.RWMutex
	entries map[string]deletingEntry
	now     func() time.Time
}

type deletingEntry struct {
	deleting bool
	expires  time.Time
}

func newProjectStatusCache() *projectStatusCache {
	return &projectStatusCache{entries: map[string]deletingEntry{}, now: time.Now}
}

// deleting reports whether the project is being torn down, reading through to
// the store at most once per TTL. An unreadable store answers "not deleting":
// the caller's own lookup reports the failure, and a platform database blip
// must not take tenant traffic down with it.
func (c *projectStatusCache) deleting(instances storage.InstanceStore, projectID string) bool {
	if instances == nil {
		return false
	}
	c.mu.RLock()
	entry, ok := c.entries[projectID]
	c.mu.RUnlock()
	if ok && c.now().Before(entry.expires) {
		return entry.deleting
	}

	inst, err := instances.FindByProjectID(projectID)
	if err != nil {
		return false
	}
	deleting := inst != nil && domain.IsDeletionStatus(inst.Status)
	c.mu.Lock()
	c.entries[projectID] = deletingEntry{deleting: deleting, expires: c.now().Add(deletingCacheTTL)}
	c.mu.Unlock()
	return deleting
}

// ProjectDeleting marks the project as being torn down without waiting for
// the TTL. The provisioning service calls it on this replica the moment it
// claims a project.
func (c *projectStatusCache) ProjectDeleting(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[projectID] = deletingEntry{deleting: true, expires: c.now().Add(deletingCacheTTL)}
}
