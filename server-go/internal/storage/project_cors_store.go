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
