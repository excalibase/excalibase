package sites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// A project scheduled for deletion is not served: its hostnames leave the
// router, while the Workers and their history stay for a cancelled deletion.
func TestUnrouteProjectSitesForgetsEveryHostnameAndKeepsTheWorkers(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"shop", "journal"} {
		site := h.createSite(t, name)
		if _, err := h.deploy(t, site, viteDist(t)); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("UnrouteProjectSites: %v", err)
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v, want none routed", h.publisher.Hosts)
	}
	if len(h.publisher.Deleted) != 0 || len(h.publisher.Published) != 2 {
		t.Fatalf("deleted %v published %v, want the Workers kept", h.publisher.Deleted, h.publisher.Published)
	}
	listed, _ := h.service.List(context.Background(), testProject)
	if len(listed) != 2 {
		t.Fatalf("sites = %d, want both kept", len(listed))
	}
}

func TestUnrouteProjectSitesReportsAHostFailure(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if _, err := h.deploy(t, site, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.publisher.FailKV = errors.New("kv down")
	var publishErr *sites.PublishError
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); !errors.As(err, &publishErr) {
		t.Fatalf("err = %v, want the CDN failure", err)
	}
}

// A cancelled deletion serves again only the sites that had a live deploy.
func TestRouteProjectSitesRoutesTheLiveSitesAgain(t *testing.T) {
	h := newHarness(t)
	live := h.createSite(t, "shop")
	if _, err := h.deploy(t, live, viteDist(t)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.createSite(t, "journal")
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("UnrouteProjectSites: %v", err)
	}
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("RouteProjectSites: %v", err)
	}
	want := map[string]string{"shop.excalibase.app": sites.ScriptName(live)}
	if len(h.publisher.Hosts) != 1 || h.publisher.Hosts["shop.excalibase.app"] != want["shop.excalibase.app"] {
		t.Fatalf("hosts = %v, want %v", h.publisher.Hosts, want)
	}
}

// Without a Cloudflare account a project's sites cannot be taken offline, so its deletion must not go ahead.
func TestUnrouteProjectSitesWithoutCloudflareRefusesWhenSitesRemain(t *testing.T) {
	store := sitestest.NewMemoryStore()
	if err := store.Create(context.Background(), &sites.Site{ID: "s1", ProjectID: testProject, Name: "shop", Hostname: "shop.excalibase.app"}, 5); err != nil {
		t.Fatal(err)
	}
	service, err := sites.NewService(store, nil, sitestest.NewOrigins(), sites.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.UnrouteProjectSites(context.Background(), testProject); !errors.Is(err, sites.ErrSitesUnavailable) {
		t.Fatalf("err = %v, want ErrSitesUnavailable", err)
	}
	if err := service.UnrouteProjectSites(context.Background(), "proj-without-sites"); err != nil {
		t.Fatalf("a project without sites has nothing to take offline: %v", err)
	}
}
