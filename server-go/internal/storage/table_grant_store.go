package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TableGrantStore persists the per-project exposure list (EXC-370) that
// excalibase-graphql reads to decide which tables and functions its
// generated GraphQL / REST surface may reach.
//
// The store holds grants and nothing else. Whether exposure is enforced is
// not per-project state and is not persisted here (EXC-400): it is on for
// every project, and the only switch is the installation-wide
// config.AppConfig.ExposureEnforced.
type TableGrantStore interface {
	// ListGrants returns the project's grants ordered by resource, role.
	// Empty (never nil) when the project has none.
	ListGrants(ctx context.Context, projectID string) ([]domain.TableGrant, error)
	// GetGrant returns one grant by id, scoped to the project.
	// Returns an error matching ErrGrantNotFound when absent.
	GetGrant(ctx context.Context, projectID, id string) (*domain.TableGrant, error)
	// UpsertGrant inserts or replaces a grant. A grant id owned by another
	// project is never overwritten.
	UpsertGrant(ctx context.Context, g *domain.TableGrant) error
	// DeleteGrant removes one grant, scoped to the project.
	DeleteGrant(ctx context.Context, projectID, id string) error
}
