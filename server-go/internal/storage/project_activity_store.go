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
	// the given source, replacing any earlier marker.
	TouchProjectActivity(ctx context.Context, projectID, source string, seenAt time.Time) error
	// GetProjectActivity returns the marker for one project. The bool is
	// false (with nil error) when the project has never been seen.
	GetProjectActivity(ctx context.Context, projectID string) (domain.ProjectActivity, bool, error)
	// ListProjectActivity returns every marker keyed by project id.
	ListProjectActivity(ctx context.Context) (map[string]domain.ProjectActivity, error)
}
