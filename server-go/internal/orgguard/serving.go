package orgguard

import (
	"context"
	"fmt"
	"time"
)

// defaultServingTTL bounds how stale a serving answer can be: a suspension
// stops serving, and an unsuspend restores it, within this long.
const defaultServingTTL = 5 * time.Second

// maxCachedProjects bounds the per-project cache; past it the cache starts over.
const maxCachedProjects = 50000

type servingLists struct {
	orgs   map[string]bool
	at     time.Time
	loaded bool
}

type projectOwner struct {
	orgID, ownerID string
	known          bool
	at             time.Time
}

// WithClock replaces the guard's clock, for tests of the cache expiry.
func (g *Guard) WithClock(now func() time.Time) *Guard {
	g.now = now
	return g
}

// WithServingTTL sets how long CheckServing trusts what it read; zero reads every time.
func (g *Guard) WithServingTTL(ttl time.Duration) *Guard {
	g.servingTTL = ttl
	return g
}

// CheckServing refuses while the owner of the project's org is suspended. It
// keys on the org owner alone: a suspended account that merely created a
// project in a good-standing org must not take that org's project down.
// (CheckProject, for starting compute, still refuses a suspended creator.)
// Unlike CheckProject it is for what the platform serves rather than starts,
// so a platform admin's override does not apply: the engine and the auth
// service ask on behalf of nobody.
//
// An unknown project passes; the caller's own not-found stands. When the
// suspension list cannot be read the last good copy answers; with none, the
// call is refused with ErrCheckUnavailable.
func (g *Guard) CheckServing(ctx context.Context, projectID string) error {
	return g.checkServing(ctx, projectID, false)
}

// CheckServingFresh is CheckServing that trusts no cached read, for the moment
// before a write that would serve a project, and for deciding whether an
// unsuspend may restore it.
func (g *Guard) CheckServingFresh(ctx context.Context, projectID string) error {
	return g.checkServing(ctx, projectID, true)
}

// CheckServingUnlessAdmin is CheckServing for work done on a person's behalf:
// a platform admin acting on the project may still run it, as for every other
// start (EXC-843). A timer, a public request or a service call carries no
// override, so none of those is let through.
func (g *Guard) CheckServingUnlessAdmin(ctx context.Context, projectID string) error {
	if overridden(ctx) {
		return nil
	}
	return g.CheckServing(ctx, projectID)
}

// CheckServingFreshUnlessAdmin is CheckServingFresh with the admin's override.
func (g *Guard) CheckServingFreshUnlessAdmin(ctx context.Context, projectID string) error {
	if overridden(ctx) {
		return nil
	}
	return g.CheckServingFresh(ctx, projectID)
}

func (g *Guard) checkServing(ctx context.Context, projectID string, fresh bool) error {
	if g == nil {
		return nil
	}
	owner, err := g.projectOwnerOf(projectID, fresh)
	if err != nil || !owner.known {
		return err
	}
	lists, err := g.servingLists(ctx, fresh)
	if err != nil {
		return err
	}
	if lists.orgs[owner.orgID] {
		return ErrOrgSuspended
	}
	return nil
}

func (g *Guard) projectOwnerOf(projectID string, fresh bool) (projectOwner, error) {
	g.mu.Lock()
	cached, ok := g.projects[projectID]
	g.mu.Unlock()
	if ok && !fresh && g.now().Sub(cached.at) < g.servingTTL {
		return cached, nil
	}
	inst, err := g.instances.FindByProjectID(projectID)
	if err != nil {
		return projectOwner{}, fmt.Errorf("%w: read the project: %w", ErrCheckUnavailable, err)
	}
	owner := projectOwner{at: g.now()}
	if inst != nil {
		owner.orgID, owner.ownerID, owner.known = inst.OrgID, inst.OwnerID, true
	}
	g.mu.Lock()
	if len(g.projects) >= maxCachedProjects {
		g.projects = map[string]projectOwner{}
	}
	g.projects[projectID] = owner
	g.mu.Unlock()
	return owner, nil
}

func (g *Guard) servingLists(ctx context.Context, fresh bool) (servingLists, error) {
	g.mu.Lock()
	cached := g.lists
	g.mu.Unlock()
	if cached.loaded && !fresh && g.now().Sub(cached.at) < g.servingTTL {
		return cached, nil
	}
	read, err := g.readLists(ctx)
	if err != nil {
		// A fresh check is asked because the last answer is not good enough.
		if cached.loaded && !fresh {
			return cached, nil
		}
		return servingLists{}, fmt.Errorf("%w: %w", ErrCheckUnavailable, err)
	}
	g.mu.Lock()
	g.lists = read
	g.mu.Unlock()
	return read, nil
}

func (g *Guard) readLists(ctx context.Context) (servingLists, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	orgs, err := g.orgs(lookupCtx)
	if err != nil {
		return servingLists{}, err
	}
	return servingLists{orgs: orgs, at: g.now(), loaded: true}, nil
}
