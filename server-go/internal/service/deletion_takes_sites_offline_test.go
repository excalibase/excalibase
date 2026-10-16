package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// recordingSiteRouter stands in for Sites (EXC-353): it records when the
// project's sites leave and rejoin the router.
type recordingSiteRouter struct {
	pauser          *stoppingPauser
	unrouted        []string
	pausesAtUnroute []int
	routed          []string
	unrouteErr      error
	routeErr        error
}

func (r *recordingSiteRouter) UnrouteProjectSites(_ context.Context, projectID string) error {
	r.unrouted = append(r.unrouted, projectID)
	r.pausesAtUnroute = append(r.pausesAtUnroute, len(r.pauser.calls))
	return r.unrouteErr
}

func (r *recordingSiteRouter) RouteProjectSites(_ context.Context, projectID string) error {
	r.routed = append(r.routed, projectID)
	return r.routeErr
}

func newSiteRoutingGrace(t *testing.T) (*graceHarness, *recordingSiteRouter) {
	t.Helper()
	h := newGraceHarness(t)
	router := &recordingSiteRouter{pauser: h.pauser}
	h.svc.SetSiteRouter(router)
	return h, router
}

// Nothing of a project scheduled for deletion may answer: its sites go
// offline with its apps, before the backup, and again once the database is down.
func TestScheduleDeletionTakesTheSitesOffline(t *testing.T) {
	h, router := newSiteRoutingGrace(t)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if len(router.unrouted) != 2 || router.pausesAtUnroute[0] != 0 || router.pausesAtUnroute[1] != 1 {
		t.Fatalf("unrouted %v at pauses %v, want before and after the database pause", router.unrouted, router.pausesAtUnroute)
	}
	if h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status = %s, want PENDING_DELETION", h.row(t).Status)
	}
}

func TestScheduleDeletionIsRefusedWhenTheSitesCannotBeTakenOffline(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	router.unrouteErr = errors.New("kv down")

	_, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if !errors.Is(err, ErrDeletionStopFailed) {
		t.Fatalf("got %v, want ErrDeletionStopFailed", err)
	}
	if len(h.pauser.calls) != 0 || h.row(t).Status != "ACTIVE" {
		t.Fatalf("pauses %v status %s: nothing else may be stopped", h.pauser.calls, h.row(t).Status)
	}
}

func TestScheduleDeletionOfAProtectedProjectKeepsItsSitesServed(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	row := h.row(t)
	on := true
	row.DeletionProtection = &on
	_ = h.store.Update(row)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("got %v, want ErrDeletionProtected", err)
	}
	if len(router.unrouted) != 0 {
		t.Fatalf("unrouted = %v, a refused deletion takes nothing offline", router.unrouted)
	}
}

func TestCancelDeletionServesTheSitesAgain(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}

	if err := h.svc.CancelDeletion(context.Background(), graceProject); err != nil {
		t.Fatalf("CancelDeletion: %v", err)
	}
	if len(router.routed) != 1 || router.routed[0] != graceProject {
		t.Fatalf("routed = %v, want the sites served again", router.routed)
	}
}

// A cancel that cannot route the sites fails and can be retried: the project stays scheduled.
func TestCancelDeletionIsRefusedWhenTheSitesCannotBeServedAgain(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	router.routeErr = errors.New("kv down")

	if err := h.svc.CancelDeletion(context.Background(), graceProject); err == nil {
		t.Fatal("want the routing failure reported")
	}
	if h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status = %s, want still PENDING_DELETION for the retry", h.row(t).Status)
	}
}

// A cancel refused by the plan leaves the project scheduled, so its sites go offline again.
func TestCancelDeletionRefusedByThePlanLeavesTheSitesOffline(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if _, err := h.svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "replacement", OrgID: "org1", DBType: domain.PostgreSQL,
	}); err != nil {
		t.Fatal(err)
	}
	before := len(router.unrouted)

	if err := h.svc.CancelDeletion(context.Background(), graceProject); err == nil {
		t.Fatal("want the plan refusal")
	}
	if len(router.routed) != 1 || len(router.unrouted) != before+1 {
		t.Fatalf("routed %v unrouted %v, want the sites routed then taken offline again", router.routed, router.unrouted)
	}
}
