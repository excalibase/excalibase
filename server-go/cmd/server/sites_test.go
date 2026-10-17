package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func sitesConfig(zoneID string) config.AppConfig {
	return config.AppConfig{SitesEnabled: true, Sites: config.SitesConfig{
		AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "excalibase.app", HostsKVNamespaceID: "k", ZoneID: zoneID,
	}, AppDomain: "apps.example.test", PublicBaseURL: "https://api.example.test/v1", StudioURL: "https://studio.example.test"}
}

func TestSiteCustomDomainsNeedTheZone(t *testing.T) {
	if customDomainOptions(sitesConfig("")) != nil {
		t.Fatal("custom domains wired without a zone id")
	}
	withZone := sitesConfig("0123456789abcdef0123456789abcdef")
	withZone.Sites.MaxZoneHostnames = 40
	options := customDomainOptions(withZone)
	if options == nil || options.Target != "cname.excalibase.app" || options.Zone == nil || options.Resolver == nil ||
		options.MaxOnZone != 40 {
		t.Fatalf("options = %+v", options)
	}
	for _, domain := range []string{"apps.example.test", "api.example.test", "studio.example.test"} {
		if !slices.Contains(options.ReservedDomains, domain) {
			t.Errorf("%s is not reserved: %v", domain, options.ReservedDomains)
		}
	}
}

func TestConfigReportsSiteCustomDomains(t *testing.T) {
	for zone, want := range map[string]bool{"": false, "0123456789abcdef0123456789abcdef": true} {
		recorder := httptest.NewRecorder()
		serveConfig(sitesConfig(zone), features.NewStatic())(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if strings.Contains(recorder.Body.String(), `"siteCustomDomains":true`) != want {
			t.Errorf("zone %q: %s", zone, recorder.Body)
		}
	}
}

func TestProjectGateStopsAProjectBeingDeleted(t *testing.T) {
	instances := fakestore.NewInstances()
	for project, status := range map[string]string{"live": "ACTIVE", "restoring": "RESTORING", "scheduled": "PENDING_DELETION", "tearing": "DELETING"} {
		instances.Items[project] = &domain.DatabaseInstance{ProjectID: project, Status: status}
	}
	gate := projectGate{instances: instances}
	for project, want := range map[string]bool{"live": true, "restoring": true, "scheduled": false, "tearing": false, "no-row": true} {
		serving, err := gate.ProjectServing(context.Background(), project)
		if err != nil || serving != want {
			t.Errorf("%s: serving=%v err=%v, want %v", project, serving, err, want)
		}
	}
}

// fakeClaimer is the project lifecycle lease: busy while another operation holds it.
type fakeClaimer struct {
	busy    bool
	claimed []string
}

func (f *fakeClaimer) Claim(_ context.Context, projectID string, _ service.ProjectOperation) (func(), bool, error) {
	if f.busy {
		return nil, false, nil
	}
	f.claimed = append(f.claimed, projectID)
	return func() {}, true, nil
}

// A custom domain's check takes the same lease deletion, pause and resume take.
func TestProjectGateHoldsTheLifecycleLease(t *testing.T) {
	claimer := &fakeClaimer{}
	gate := projectGate{instances: fakestore.NewInstances(), claimer: claimer}
	release, held, err := gate.HoldProject(context.Background(), "p1")
	if err != nil || !held || release == nil || !slices.Equal(claimer.claimed, []string{"p1"}) {
		t.Fatalf("held=%v err=%v claimed=%v", held, err, claimer.claimed)
	}
	release()
	claimer.busy = true
	if _, held, err := gate.HoldProject(context.Background(), "p1"); err != nil || held {
		t.Fatalf("held=%v err=%v while another operation runs", held, err)
	}
	unleased := projectGate{instances: fakestore.NewInstances()}
	if release, held, err := unleased.HoldProject(context.Background(), "p1"); err != nil || !held || release == nil {
		t.Fatalf("without a claimer: held=%v err=%v", held, err)
	}
}

// Adding and removing custom domains is bounded per project: each one is Cloudflare calls on one token.
func TestSiteDomainWritesAreLimitedPerProject(t *testing.T) {
	limiter := newSiteDomainWriteLimiter()
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.With(limiter).Post("/domains", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	})
	post := func(project string) int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects/"+project+"/domains", nil))
		return recorder.Code
	}
	for i := 0; i < siteDomainWriteBudget; i++ {
		if code := post("p1"); code != http.StatusCreated {
			t.Fatalf("write %d = %d", i, code)
		}
	}
	if code := post("p1"); code != http.StatusTooManyRequests {
		t.Fatalf("write past the budget = %d, want 429", code)
	}
	if code := post("p2"); code != http.StatusCreated {
		t.Fatalf("another project's write = %d", code)
	}
}

// Checking a domain is what calls Cloudflare, so it has its own per-project budget.
func TestSiteDomainChecksAreLimitedPerProject(t *testing.T) {
	limiter := newSiteDomainCheckLimiter()
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.With(limiter).Post("/refresh", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	post := func(project string) int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects/"+project+"/refresh", nil))
		return recorder.Code
	}
	for i := 0; i < siteDomainCheckBudget; i++ {
		if code := post("p1"); code != http.StatusOK {
			t.Fatalf("check %d = %d", i, code)
		}
	}
	if code := post("p1"); code != http.StatusTooManyRequests {
		t.Fatalf("check past the budget = %d, want 429", code)
	}
	if code := post("p2"); code != http.StatusOK {
		t.Fatalf("another project's check = %d", code)
	}
}
