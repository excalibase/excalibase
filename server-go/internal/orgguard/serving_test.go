package orgguard_test

import (
	"context"
	"errors"
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
		return *orgs, nil
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
	users["creator"] = true
	if err := g.CheckServing(ctx, "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("suspended creator: %v", err)
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
