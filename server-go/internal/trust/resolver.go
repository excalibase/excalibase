package trust

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// ErrUnresolved means the owner of a project could not be found. The state
// returned beside it is Limited: with the feature on, anything unknown is limited.
var ErrUnresolved = errors.New("trust: owner could not be resolved")

// ProjectInstances finds a project's instance, honouring the caller's context.
type ProjectInstances interface {
	FindByProjectIDContext(ctx context.Context, projectID string) (*domain.DatabaseInstance, error)
}

// OrgFinder finds an org by id.
type OrgFinder interface {
	FindOrgByID(ctx context.Context, id string) (*domain.Org, error)
}

// Resolver answers the trust of whoever owns something. "Owner" is the org's
// immutable OwnerID; a limited member of a full owner's org runs at full.
// It is the only place that reads trust, so every consumer fails closed alike.
type Resolver struct {
	flags     features.Flags
	store     Store
	instances ProjectInstances
	orgs      OrgFinder
}

func NewResolver(flags features.Flags, store Store, instances ProjectInstances, orgs OrgFinder) *Resolver {
	return &Resolver{flags: flags, store: store, instances: instances, orgs: orgs}
}

func (r *Resolver) enabled(ctx context.Context) bool {
	return features.Enabled(ctx, r.flags, features.LimitedTrial)
}

// ForOwner is the trust of one account. Feature off: Full, and nothing is read.
// Feature on: Full only for an account whose stored state says exactly so;
// no row, an unknown state or any error is Limited.
func (r *Resolver) ForOwner(ctx context.Context, ownerID string) (State, error) {
	if !r.enabled(ctx) {
		return Full, nil
	}
	if ownerID == "" {
		return Limited, fmt.Errorf("%w: no owner id", ErrUnresolved)
	}
	record, err := r.store.Find(ctx, ownerID)
	if err != nil {
		return Limited, fmt.Errorf("trust: read decision: %w", err)
	}
	if record == nil {
		return Limited, nil
	}
	switch record.State {
	case Full:
		return Full, nil
	case Limited:
		return Limited, nil
	default:
		return Limited, fmt.Errorf("%w: stored state %q for %s", ErrInvalidRecord, record.State, ownerID)
	}
}

// ForProject is the trust of the owner of the project's org.
func (r *Resolver) ForProject(ctx context.Context, projectID string) (State, error) {
	if !r.enabled(ctx) {
		return Full, nil
	}
	instance, err := r.instances.FindByProjectIDContext(ctx, projectID)
	if err != nil {
		return Limited, fmt.Errorf("%w: project: %w", ErrUnresolved, err)
	}
	if instance == nil || instance.OrgID == "" {
		return Limited, fmt.Errorf("%w: project has no organisation", ErrUnresolved)
	}
	org, err := r.orgs.FindOrgByID(ctx, instance.OrgID)
	if err != nil {
		return Limited, fmt.Errorf("%w: organisation: %w", ErrUnresolved, err)
	}
	if org == nil {
		return Limited, fmt.Errorf("%w: organisation missing", ErrUnresolved)
	}
	return r.ForOwner(ctx, org.OwnerID)
}
