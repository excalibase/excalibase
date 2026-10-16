package sites_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// Publishing must finish inside the lease, or a delete that took the site over could be undone by it.
func TestPublishRunsUnderADeadlineInsideTheLease(t *testing.T) {
	h := newHarness(t)
	var deadline time.Time
	h.publisher.BeforePut = func(ctx context.Context) {
		deadline, _ = ctx.Deadline()
	}
	site := h.createSite(t, "shop")
	started := time.Now()
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if deadline.IsZero() || !deadline.Before(started.Add(sites.MaxDeployDuration)) {
		t.Fatalf("deadline = %v, want one before the lease ends at %v", deadline, started.Add(sites.MaxDeployDuration))
	}
}

func TestADeployThatLostItsLeaseDoesNotGoLive(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	h.publisher.BeforePut = func(context.Context) {
		h.store.ExpireLease(site.ID)
		if err := h.store.ClaimDeploy(context.Background(), testProject, site.ID, "other-holder", time.Now().Add(time.Minute)); err != nil {
			t.Errorf("take over: %v", err)
		}
	}
	if _, err := h.deploy(t, site, viteDist(t)); !errors.Is(err, sites.ErrLeaseLost) {
		t.Fatalf("err = %v, want the lease lost", err)
	}
	stored, _ := h.store.Get(context.Background(), testProject, site.ID)
	if stored.LiveDeployID != "" {
		t.Fatalf("live = %q after the lease was lost", stored.LiveDeployID)
	}
	deploys, _ := h.service.ListDeploys(context.Background(), testProject, site.ID)
	if len(deploys) != 1 || deploys[0].Status != sites.DeployFailed {
		t.Fatalf("deploys = %+v, want one FAILED", deploys)
	}
	if h.store.LeaseHolder(site.ID) != "other-holder" {
		t.Fatalf("the stale deploy released the new holder's lease: %q", h.store.LeaseHolder(site.ID))
	}
}

// A caller that hangs up after the CDN switched must still leave the database pointing at it.
func TestRollbackRecordsTheLiveDeployAfterTheCallerLeaves(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	first, err := h.deploy(t, site, viteDist(t))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := h.deploy(t, site, distWith(t, "<h1>v2</h1>")); err != nil {
		t.Fatalf("second: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.publisher.BeforePut = func(context.Context) { cancel() }
	_, _ = h.service.Rollback(ctx, testProject, site.ID, first.ID)
	stored, _ := h.store.Get(context.Background(), testProject, site.ID)
	if stored.LiveDeployID != first.ID {
		t.Fatalf("live = %q, want %q", stored.LiveDeployID, first.ID)
	}
}
