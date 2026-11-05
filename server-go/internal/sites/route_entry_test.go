package sites_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// The router (edge/sites-router) reads these same files in its contract test,
// so a change on either side of SITE_HOSTS fails one of the two suites.
var updateRouteFixtures = flag.Bool("update-route-fixtures", false, "rewrite testdata/router-entry-*")

func serverPlanLimits() sites.Limits {
	limits := sites.DefaultLimits
	limits.MaxServerCPUMillis, limits.MaxServerSubrequests = 75, 30
	return limits
}

type routedEntry struct {
	Script      string `json:"script"`
	Project     string `json:"project"`
	CPUMs       int    `json:"cpuMs"`
	SubRequests int    `json:"subRequests"`
	Paused      *bool  `json:"paused"`
}

func wantServerEntry(t *testing.T, value string, site *sites.Site) {
	t.Helper()
	wantServerEntryUnder(t, value, site, serverPlanLimits())
}

func wantServerEntryUnder(t *testing.T, value string, site *sites.Site, limits sites.Limits) {
	t.Helper()
	var entry routedEntry
	if err := json.Unmarshal([]byte(value), &entry); err != nil {
		t.Fatalf("routed %q, want a server site's JSON entry: %v", value, err)
	}
	if entry.Script != sites.ScriptName(site) || entry.Project != site.ProjectID ||
		entry.CPUMs != limits.MaxServerCPUMillis || entry.SubRequests != limits.MaxServerSubrequests ||
		entry.Paused == nil || *entry.Paused {
		t.Fatalf("entry = %s", value)
	}
}

func newPlanServerHarness(t *testing.T) *serviceHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	options := serverOptions(t, features.NewStatic(features.SitesServer), store)
	options.DeployLimits = fixedLimits{limits: serverPlanLimits()}
	service, err := sites.NewService(store, publisher, origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
}

func TestAServerSiteIsRoutedWithItsProjectAndPlanLimits(t *testing.T) {
	h := newPlanServerHarness(t)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	wantServerEntry(t, h.publisher.Hosts[site.Hostname], site)
}

// The timer's reconcile rewrites every live server site, which also moves an
// entry written in the old bare-script format onto the JSON one.
func TestReconcileRewritesAServerSitesEntry(t *testing.T) {
	h := newPlanServerHarness(t)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.publisher.Hosts[site.Hostname] = sites.ScriptName(site)
	if err := h.service.ReconcileServerSites(context.Background()); err != nil {
		t.Fatalf("ReconcileServerSites: %v", err)
	}
	wantServerEntry(t, h.publisher.Hosts[site.Hostname], site)
}

func TestRoutingAProjectAgainWritesAServerSitesEntry(t *testing.T) {
	h := newPlanServerHarness(t)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := h.service.UnrouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("UnrouteProjectSites: %v", err)
	}
	if err := h.service.RouteProjectSites(context.Background(), testProject); err != nil {
		t.Fatalf("RouteProjectSites: %v", err)
	}
	wantServerEntry(t, h.publisher.Hosts[site.Hostname], site)
}

func TestACustomDomainOfAServerSiteIsRoutedWithItsEntry(t *testing.T) {
	h := newDomainHarnessWith(t, func(options *sites.Options) {
		options.Flags = features.NewStatic(features.SitesServer)
		options.OutboundWorker = "excalibase-sites-outbound"
		options.Outbound = &sitestest.Outbound{Worker: "excalibase-sites-outbound"}
		options.DeployLimits = fixedLimits{limits: serverPlanLimits()}
	})
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	added := h.add(t, site, "www.example.com")
	h.zone.SetState("www.example.com", "active", "active")
	h.refresh(t, site, added.ID)
	wantServerEntry(t, h.publisher.Hosts["www.example.com"], site)
}

func TestARouteEntryNeedsTheServerLimits(t *testing.T) {
	site := &sites.Site{ID: "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c", ProjectID: "proj-shop1", Kind: sites.KindServer}
	if _, err := sites.RouteEntry(site, sites.Limits{}); err == nil {
		t.Fatal("a server site was routed without its CPU and subrequest limits")
	}
}

// TestRouteEntriesMatchTheRouterContract pins the exact SITE_HOSTS values the
// platform writes; edge/sites-router/test/contract.test.js reads the same files.
func TestRouteEntriesMatchTheRouterContract(t *testing.T) {
	const siteID, project = "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c", "proj-shop1"
	limits := sites.DefaultLimits
	cases := map[string]*sites.Site{
		"router-entry-server.json": {ID: siteID, ProjectID: project, Kind: sites.KindServer},
		"router-entry-static.txt":  {ID: siteID, ProjectID: project, Kind: sites.KindStatic},
	}
	for name, site := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := sites.RouteEntry(site, limits)
			if err != nil {
				t.Fatalf("RouteEntry: %v", err)
			}
			path := filepath.Join("testdata", name)
			if *updateRouteFixtures {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s (run with -update-route-fixtures to create it): %v", path, err)
			}
			if got != string(want) {
				t.Fatalf("RouteEntry = %s, fixture %s = %s", got, path, want)
			}
		})
	}
}
