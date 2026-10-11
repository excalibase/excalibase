package storage

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ProjectActivityStore persists the last-seen marker per project. Writes are
// expected to be throttled by the caller (service.ActivityRecorder) — the
// store itself is a plain upsert.
type ProjectActivityStore interface {
	// TouchProjectActivity records that the project was seen at seenAt via
	// the given source, replacing any earlier marker and clearing an
	// outstanding idle warning.
	TouchProjectActivity(ctx context.Context, projectID, source string, seenAt time.Time) error
	// MarkIdleWarned stamps the idle warning. When the project has no row yet
	// one is inserted anchored at lastSeen (the caller's fallback, normally
	// created_at); an existing row keeps its last_seen_at.
	MarkIdleWarned(ctx context.Context, projectID string, lastSeen, warnedAt time.Time) error
	// GetProjectActivity returns the marker for one project. The bool is
	// false (with nil error) when the project has never been seen.
	GetProjectActivity(ctx context.Context, projectID string) (domain.ProjectActivity, bool, error)
	// ListProjectActivity returns every marker keyed by project id.
	ListProjectActivity(ctx context.Context) (map[string]domain.ProjectActivity, error)
}

// DatabaseWorkStore keeps the last reading of a project's client work, read
// from its database by the idle sweep (EXC-588).
type DatabaseWorkStore interface {
	// DatabaseWork is the last stored reading; false when there is none.
	DatabaseWork(ctx context.Context, projectID string) (int64, bool, error)
	// SaveDatabaseWork stores a reading in place of the last one.
	SaveDatabaseWork(ctx context.Context, projectID string, work int64, readAt time.Time) error
}
