// Package orgguard refuses to start compute for an organisation whose owner
// is abuse-suspended (EXC-843). The admin console's suspension stops the
// owner's apps; without this check any other member could start them again.
package orgguard

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Message is what the caller reads: plain, with no reason or account detail.
const Message = "This organisation is suspended. Contact support."

// ErrOrgSuspended is returned for any action that would start or restart compute.
var ErrOrgSuspended = errors.New(Message) //nolint:staticcheck // shown to the customer as a sentence

// SuspendedOrgs lists the ids of orgs whose owner is abuse-suspended.
type SuspendedOrgs func(ctx context.Context) (map[string]bool, error)

type overrideKey struct{}

// WithPlatformOverride marks ctx as acting for a platform admin, who may
// resume work in a suspended organisation.
func WithPlatformOverride(ctx context.Context) context.Context {
	return context.WithValue(ctx, overrideKey{}, true)
}

func overridden(ctx context.Context) bool {
	v, _ := ctx.Value(overrideKey{}).(bool)
	return v
}

// Guard answers whether an org's owner is suspended. A nil Guard allows everything.
type Guard struct {
	instances storage.InstanceStore
	suspended SuspendedOrgs
}

func New(instances storage.InstanceStore, suspended SuspendedOrgs) *Guard {
	return &Guard{instances: instances, suspended: suspended}
}

// CheckProject refuses when the project's org owner is suspended. An unknown
// project passes, so the caller's own not-found answer stands. A lookup that
// fails refuses too: a suspension must not lapse because a read did.
func (g *Guard) CheckProject(ctx context.Context, projectID string) error {
	if g == nil || overridden(ctx) {
		return nil
	}
	inst, err := g.instances.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("check the organisation is not suspended: %w", err)
	}
	if inst == nil {
		return nil
	}
	return g.CheckOrg(ctx, inst.OrgID)
}

// CheckOrg refuses when the org's owner is suspended.
func (g *Guard) CheckOrg(ctx context.Context, orgID string) error {
	if g == nil || overridden(ctx) || orgID == "" {
		return nil
	}
	orgs, err := g.suspended(ctx)
	if err != nil {
		return fmt.Errorf("check the organisation is not suspended: %w", err)
	}
	if orgs[orgID] {
		return ErrOrgSuspended
	}
	return nil
}
