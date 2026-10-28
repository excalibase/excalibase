package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DBSourceAllowListStore keeps each project's database allow-list (EXC-665).
type DBSourceAllowListStore interface {
	// GetDBSourceAllowList returns the project's choice; a project that never
	// chose reads as DBSourceAccessUnset with no sources.
	GetDBSourceAllowList(ctx context.Context, projectID string) (domain.DBSourceAllowList, error)
	// SetDBSourceAllowList records a parsed, chosen list.
	SetDBSourceAllowList(ctx context.Context, list domain.DBSourceAllowList) (domain.DBSourceAllowList, error)
	// DeleteDBSourceAllowList forgets the project's choice; absent is fine.
	DeleteDBSourceAllowList(ctx context.Context, projectID string) error
}
