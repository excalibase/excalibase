package storage

import "context"

// ProjectCorsStore persists the per-project browser-origin allowlist the
// data plane (excalibase-graphql) enforces as CORS (EXC-23). Entries are
// stored already canonical — domain.ParseCorsOrigins runs before any write.
type ProjectCorsStore interface {
	// GetCorsOrigins returns the stored allowlist (empty, never nil, when unset).
	GetCorsOrigins(ctx context.Context, projectID string) ([]string, error)
	// SetCorsOrigins replaces the allowlist. nil clears it.
	SetCorsOrigins(ctx context.Context, projectID string, origins []string) error
}

// ProjectCorsEditor changes one origin at a time under the row's lock, so an
// edit never loses one made at the same moment (EXC-544). Origins are
// canonical already.
type ProjectCorsEditor interface {
	// AddCorsOrigin adds origin unless it is there or the list is the
	// wildcard. A non-empty appID records that the app added it.
	AddCorsOrigin(ctx context.Context, projectID, origin, appID string) (added bool, origins []string, err error)
	// RemoveCorsOrigin removes origin when it is there.
	RemoveCorsOrigin(ctx context.Context, projectID, origin string) (removed bool, origins []string, err error)
	// ReleaseAppCorsOrigins removes the origins appID added, answering them.
	ReleaseAppCorsOrigins(ctx context.Context, projectID, appID string) (released []string, err error)
}
