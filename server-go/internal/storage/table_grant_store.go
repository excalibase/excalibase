package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TableGrantStore persists the per-project exposure list (EXC-370) that
// excalibase-graphql reads to decide which tables and functions its
// generated GraphQL / REST surface may reach.
//
// Enforcement is stored separately from the grants themselves so consumers
// can distinguish "exposure not configured" from "configured with nothing
// granted" — an empty grant slice carries no meaning on its own.
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

	// IsExposureEnforced reports whether the project opted into exposure
	// enforcement. A project with no setting row reads as false, which keeps
	// projects that predate EXC-370 unenforced after an upgrade.
	IsExposureEnforced(ctx context.Context, projectID string) (bool, error)
	// SetExposureEnforced records the project's enforcement flag.
	SetExposureEnforced(ctx context.Context, projectID string, enforced bool) error
}
