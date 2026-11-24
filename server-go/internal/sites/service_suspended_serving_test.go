package sites_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync/atomic"
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

func flippableGuard(flag *atomic.Bool) *orgguard.Guard {
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		return map[string]bool{"org-1": flag.Load()}, nil
	})
}

// A deploy already running when the suspension lands must not route the site:
// the check sits immediately before the route write, not only at the start.
func TestDeploy_SuspensionLandingMidDeployNeverRoutesTheSite(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	var suspended atomic.Bool
	guarded, err := sites.NewService(h.store, h.publisher, h.origins,
		sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), OrgGuard: flippableGuard(&suspended)})
	if err != nil {
		t.Fatal(err)
	}
	h.publisher.BeforePut = func(context.Context) { suspended.Store(true) }

	_, err = guarded.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t))})
	if err == nil {
		t.Fatal("a deploy whose owner was suspended before it routed must fail")
	}
	if strings.Contains(err.Error(), "org-1") {
		t.Fatalf("the failure names internals: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("the site was routed after the suspension landed")
	}
}

// Previews are routed by their own write; the same rule holds.
func TestDeployPreview_SuspensionLandingMidDeployNeverRoutesIt(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	var suspended atomic.Bool
	guarded, err := sites.NewService(h.store, h.publisher, h.origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		DeployLimits: previewLimits{max: 5}, Previews: h.previews, MaxPreviews: 100, OrgGuard: flippableGuard(&suspended)})
	if err != nil {
		t.Fatal(err)
	}
	h.publisher.BeforePut = func(context.Context) { suspended.Store(true) }

	_, derr := guarded.DeployPreview(context.Background(), sites.PreviewDeployRequest{ProjectID: testProject, SiteID: site.ID,
		PreviewID: preview.ID, CommitSHA: "abc1234def", Body: bytes.NewReader(sitestest.Dist(t, "<h1>x</h1>"))})
	if len(h.publisher.Published) == 0 {
		t.Fatalf("precondition: the preview reached its publish, where the suspension lands (err %v)", derr)
	}
	if _, routed := h.publisher.Hosts[preview.Hostname]; routed {
		t.Fatal("the preview was routed after the suspension landed")
	}
}

// The reconciler takes static sites and previews of a suspended owner off the
// router, not only server sites: what suspension unrouted must stay unrouted.
func TestReconcile_UnroutesStaticSitesOfASuspendedOwner(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatal(err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; !routed {
		t.Fatal("precondition: the live static site is routed")
	}
	guarded := guardedOver(t, h, true)

	if err := guarded.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("the reconciler left a suspended owner's static site routed")
	}
	if !slices.Contains(h.publisher.Calls, "delete-host") {
		t.Fatalf("calls = %v, want an explicit unroute", h.publisher.Calls)
	}
}

// A server site that is routed when the suspension lands is taken off by the
// reconciler itself, not merely left unrouted if it was already gone.
func TestReconcileServerSites_UnroutesARoutedServerSiteOfASuspendedOwner(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; !routed {
		t.Fatal("precondition: the server site is routed")
	}
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	options := serverOptions(t, features.NewStatic(features.SitesServer), h.store)
	options.OrgGuard = orgguard.New(instances, func(context.Context) (map[string]bool, error) { return map[string]bool{"org-1": true}, nil })
	guarded, err := sites.NewService(h.store, h.publisher, h.origins, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := guarded.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, routed := h.publisher.Hosts[site.Hostname]; routed {
		t.Fatal("the reconciler left a suspended owner's routed server site on the router")
	}
}
