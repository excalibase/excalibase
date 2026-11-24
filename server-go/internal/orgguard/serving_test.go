package orgguard_test

import (
	"context"
	"errors"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func servingGuard(orgs, users *map[string]bool, failing *atomic.Bool, reads *atomic.Int32) *orgguard.Guard {
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1", OwnerID: "creator"}
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		reads.Add(1)
		if failing.Load() {
			return nil, errors.New("db down")
		}
		return maps.Clone(*orgs), nil
	}).WithSuspendedUsers(func(context.Context) (map[string]bool, error) { return *users, nil })
}

func TestCheckServing(t *testing.T) {
	ctx := context.Background()
	var failing atomic.Bool
	var reads atomic.Int32
	orgs, users := map[string]bool{}, map[string]bool{}
	g := servingGuard(&orgs, &users, &failing, &reads).WithServingTTL(0)

	if err := g.CheckServing(ctx, "p1"); err != nil {
		t.Fatalf("healthy org: %v", err)
	}
	orgs["o1"] = true
	if err := g.CheckServing(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("suspended owner: %v, want ErrOrgSuspended", err)
	}
	if err := g.CheckServing(orgguard.WithPlatformOverride(ctx), "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("a platform override must not keep a suspended project serving: %v", err)
	}
	delete(orgs, "o1")
	if err := g.CheckServing(ctx, "p1"); err != nil {
		t.Fatalf("unsuspended: %v, want serving again", err)
	}
	// A suspended creator does not take a good-standing org's project down; it
	// only stops that account starting new compute there.
	users["creator"] = true
	if err := g.CheckServing(ctx, "p1"); err != nil {
		t.Fatalf("suspended creator: %v, want the org's project to keep serving", err)
	}
	if err := g.CheckProject(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("suspended creator starting compute: %v, want refused", err)
	}
	if err := g.CheckServing(ctx, "missing"); err != nil {
		t.Fatalf("unknown project: %v", err)
	}
	var none *orgguard.Guard
	if err := none.CheckServing(ctx, "p1"); err != nil {
		t.Fatalf("nil guard: %v", err)
	}
}

func TestCheckServing_ReadsOncePerTTLAndKeepsTheLastAnswerWhenTheReadFails(t *testing.T) {
	ctx := context.Background()
	var failing atomic.Bool
	var reads atomic.Int32
	orgs, users := map[string]bool{"o1": true}, map[string]bool{}
	g := servingGuard(&orgs, &users, &failing, &reads)

	for range 5 {
		if err := g.CheckServing(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
			t.Fatalf("err = %v", err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("suspension list read %d times for 5 calls inside the TTL, want 1", reads.Load())
	}

	g = servingGuard(&orgs, &users, &failing, &reads).WithServingTTL(time.Nanosecond)
	if err := g.CheckServing(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v", err)
	}
	failing.Store(true)
	time.Sleep(time.Millisecond)
	if err := g.CheckServing(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("a failed read must keep the last answer: %v", err)
	}
}

func TestCheckServing_RefusesWhenNothingWasEverRead(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	var reads atomic.Int32
	orgs, users := map[string]bool{}, map[string]bool{}
	err := servingGuard(&orgs, &users, &failing, &reads).CheckServing(context.Background(), "p1")
	if !errors.Is(err, orgguard.ErrCheckUnavailable) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
}

func TestCheckServingUnlessAdmin(t *testing.T) {
	var failing atomic.Bool
	var reads atomic.Int32
	orgs, users := map[string]bool{"o1": true}, map[string]bool{}
	g := servingGuard(&orgs, &users, &failing, &reads).WithServingTTL(0)

	if err := g.CheckServingUnlessAdmin(context.Background(), "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("no override: %v, want ErrOrgSuspended", err)
	}
	if err := g.CheckServingUnlessAdmin(orgguard.WithPlatformOverride(context.Background()), "p1"); err != nil {
		t.Fatalf("a platform admin acting on the project: %v, want allowed", err)
	}
}

type countingInstances struct {
	*fakestore.Instances
	reads atomic.Int32
}

func (c *countingInstances) FindByProjectID(id string) (*domain.DatabaseInstance, error) {
	c.reads.Add(1)
	return c.Instances.FindByProjectID(id)
}

func TestCheckServing_CachesExpireOnTheClock(t *testing.T) {
	ctx := context.Background()
	instances := &countingInstances{Instances: fakestore.NewInstances()}
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1"}
	var listReads atomic.Int32
	suspended := map[string]bool{}
	now := time.Unix(1_000, 0)
	g := orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		listReads.Add(1)
		return maps.Clone(suspended), nil
	}).WithServingTTL(5 * time.Second).WithClock(func() time.Time { return now })

	check := func() error { return g.CheckServing(ctx, "p1") }
	if err := check(); err != nil {
		t.Fatal(err)
	}
	suspended["o1"] = true
	now = now.Add(4 * time.Second)
	if err := check(); err != nil {
		t.Fatalf("inside the TTL the cached answer stands: %v", err)
	}
	if instances.reads.Load() != 1 || listReads.Load() != 1 {
		t.Fatalf("reads inside the TTL: project %d, list %d, want 1 and 1", instances.reads.Load(), listReads.Load())
	}
	now = now.Add(2 * time.Second)
	if err := check(); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("past the TTL the list is read again: %v, want ErrOrgSuspended", err)
	}
	if instances.reads.Load() != 2 || listReads.Load() != 2 {
		t.Fatalf("reads past the TTL: project %d, list %d, want 2 and 2", instances.reads.Load(), listReads.Load())
	}
	// The project moved to another org: the owner cache expires too.
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o2"}
	if err := check(); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("inside the TTL the owner is cached: %v", err)
	}
	now = now.Add(6 * time.Second)
	if err := check(); err != nil {
		t.Fatalf("past the TTL the owner is read again: %v, want serving", err)
	}
}

func TestCheckServingFresh_TrustsNoCacheAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	var failing atomic.Bool
	var reads atomic.Int32
	orgs, users := map[string]bool{}, map[string]bool{}
	g := servingGuard(&orgs, &users, &failing, &reads).WithServingTTL(time.Hour)

	if err := g.CheckServing(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	orgs["o1"] = true
	if err := g.CheckServing(ctx, "p1"); err != nil {
		t.Fatalf("cached: %v", err)
	}
	if err := g.CheckServingFresh(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("fresh: %v, want ErrOrgSuspended past the cache", err)
	}
	failing.Store(true)
	if err := g.CheckServingFresh(ctx, "p1"); !errors.Is(err, orgguard.ErrCheckUnavailable) {
		t.Fatalf("fresh with the read down: %v, want ErrCheckUnavailable, not the stale copy", err)
	}
}
