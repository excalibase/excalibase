package sites_test

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// A server site runs the customer's code on Cloudflare per request. The
// reconciler routes every live server site on a timer and never asks whether
// the org owner is suspended, so a suspended abuser's server code keeps
// serving (and is re-routed if anything unrouted it).
func TestReconcileServerSites_DoesNotServeASuspendedOrg(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	options := serverOptions(t, features.NewStatic(features.SitesServer), h.store)
	options.OrgGuard = orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		return map[string]bool{"org-1": true}, nil
	})
	guarded, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	delete(h.publisher.Hosts, site.Hostname) // e.g. an admin unrouted it after the suspension

	if err := guarded.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("the reconciler routed a suspended org's server site")
	}
}

// RouteProjectSites (deletion cancel) likewise serves every live site again
// without asking whether the owner is suspended.
func TestRouteProjectSites_DoesNotServeASuspendedOrg(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	guarded := guardedOver(t, h, true)
	delete(h.publisher.Hosts, site.Hostname)

	if err := guarded.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("RouteProjectSites: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("a suspended org's site was routed again")
	}
}
