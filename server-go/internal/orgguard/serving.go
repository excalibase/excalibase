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
	orgs, users map[string]bool
	at          time.Time
	loaded      bool
}

type projectOwner struct {
	orgID, ownerID string
	known          bool
	at             time.Time
}

// WithServingTTL sets how long CheckServing trusts what it read; zero reads every time.
func (g *Guard) WithServingTTL(ttl time.Duration) *Guard {
	g.servingTTL = ttl
	return g
}

// CheckServing refuses while the project's org owner, or the account that
// created it, is suspended. Unlike CheckProject it is for what the platform
// serves rather than starts, so a platform admin's override does not apply:
// the engine and the auth service ask on behalf of nobody.
//
// An unknown project passes; the caller's own not-found stands. When the
// suspension lists cannot be read the last good copy answers; with none, the
// call is refused.
func (g *Guard) CheckServing(ctx context.Context, projectID string) error {
	if g == nil {
		return nil
	}
	owner, err := g.projectOwnerOf(projectID)
	if err != nil || !owner.known {
		return err
	}
	lists, err := g.servingLists(ctx)
	if err != nil {
		return err
	}
	if lists.orgs[owner.orgID] || (owner.ownerID != "" && lists.users[owner.ownerID]) {
		return ErrOrgSuspended
	}
	return nil
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

func (g *Guard) projectOwnerOf(projectID string) (projectOwner, error) {
	g.mu.Lock()
	cached, ok := g.projects[projectID]
	g.mu.Unlock()
	if ok && g.now().Sub(cached.at) < g.servingTTL {
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

func (g *Guard) servingLists(ctx context.Context) (servingLists, error) {
	g.mu.Lock()
	cached := g.lists
	g.mu.Unlock()
	if cached.loaded && g.now().Sub(cached.at) < g.servingTTL {
		return cached, nil
	}
	fresh, err := g.readLists(ctx)
	if err != nil {
		if cached.loaded {
			return cached, nil
		}
		return servingLists{}, fmt.Errorf("%w: %w", ErrCheckUnavailable, err)
	}
	g.mu.Lock()
	g.lists = fresh
	g.mu.Unlock()
	return fresh, nil
}

func (g *Guard) readLists(ctx context.Context) (servingLists, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	orgs, err := g.orgs(lookupCtx)
	if err != nil {
		return servingLists{}, err
	}
	users := map[string]bool{}
	if g.users != nil {
		if users, err = g.users(lookupCtx); err != nil {
			return servingLists{}, err
		}
	}
	return servingLists{orgs: orgs, users: users, at: g.now(), loaded: true}, nil
}
