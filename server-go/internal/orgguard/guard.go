// Package orgguard refuses to start compute for an organisation whose owner
// is abuse-suspended (EXC-843). The admin console's suspension stops the
// owner's apps; without this check any other member could start them again.
package orgguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Message is what the caller reads: plain, with no reason or account detail.
const Message = "This organisation is suspended. Contact support."

// ErrOrgSuspended is returned for any action that would start or restart compute.
var ErrOrgSuspended = errors.New(Message) //nolint:staticcheck // shown to the customer as a sentence

// ErrCheckUnavailable means the suspension could not be read. The action is
// refused: a suspension must not lapse because a read failed.
var ErrCheckUnavailable = errors.New("the organisation's standing could not be checked; try again")

// IsRefusal reports whether err is one of the guard's refusals.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrOrgSuspended) || errors.Is(err, ErrCheckUnavailable)
}

const defaultLookupTimeout = 3 * time.Second

// SuspendedOrgs lists the ids of orgs whose owner is abuse-suspended.
type SuspendedOrgs func(ctx context.Context) (map[string]bool, error)

// SuspendedUsers lists the ids of abuse-suspended accounts.
type SuspendedUsers func(ctx context.Context) (map[string]bool, error)

type overrideKey struct{}

// WithPlatformOverride marks ctx as acting for a platform admin, who may
// resume work in a suspended organisation.
func WithPlatformOverride(ctx context.Context) context.Context {
	return context.WithValue(ctx, overrideKey{}, true)
}

// Overridden reports whether ctx acts for a platform admin.
func Overridden(ctx context.Context) bool { return overridden(ctx) }

func overridden(ctx context.Context) bool {
	v, _ := ctx.Value(overrideKey{}).(bool)
	return v
}

// Guard answers whether a project belongs to a suspended account. A nil Guard allows everything.
type Guard struct {
	instances storage.InstanceStore
	orgs      SuspendedOrgs
	users     SuspendedUsers
	timeout   time.Duration

	// serving state: CheckServing sits on request paths, so it reads the
	// suspension lists and each project's owner at most once per servingTTL.
	servingTTL time.Duration
	now        func() time.Time
	mu         sync.Mutex
	lists      servingLists
	projects   map[string]projectOwner
}

func New(instances storage.InstanceStore, orgs SuspendedOrgs) *Guard {
	return &Guard{instances: instances, orgs: orgs, timeout: defaultLookupTimeout,
		servingTTL: defaultServingTTL, now: time.Now, projects: map[string]projectOwner{}}
}

// WithSuspendedUsers also refuses projects an abuse-suspended account created
// in someone else's org: the suspension stops those apps as well.
func (g *Guard) WithSuspendedUsers(users SuspendedUsers) *Guard {
	g.users = users
	return g
}

// WithLookupTimeout bounds each suspension read.
func (g *Guard) WithLookupTimeout(timeout time.Duration) *Guard {
	g.timeout = timeout
	return g
}

// CheckProject refuses when the project's org owner or creator is suspended.
// An unknown project passes, so the caller's own not-found answer stands; a
// read that fails refuses.
func (g *Guard) CheckProject(ctx context.Context, projectID string) error {
	if g == nil || overridden(ctx) {
		return nil
	}
	inst, err := g.instances.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("%w: read the project: %w", ErrCheckUnavailable, err)
	}
	if inst == nil {
		return nil
	}
	if err := g.CheckOrg(ctx, inst.OrgID); err != nil {
		return err
	}
	return g.checkCreator(ctx, inst.OwnerID)
}

// CheckOrg refuses when the org's owner is suspended.
func (g *Guard) CheckOrg(ctx context.Context, orgID string) error {
	if g == nil || overridden(ctx) || orgID == "" {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	orgs, err := g.orgs(lookupCtx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckUnavailable, err)
	}
	if orgs[orgID] {
		return ErrOrgSuspended
	}
	return nil
}

func (g *Guard) checkCreator(ctx context.Context, ownerID string) error {
	if g.users == nil || ownerID == "" {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	users, err := g.users(lookupCtx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckUnavailable, err)
	}
	if users[ownerID] {
		return ErrOrgSuspended
	}
	return nil
}
