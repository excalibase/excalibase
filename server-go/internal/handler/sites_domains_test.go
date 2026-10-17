package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
	"github.com/go-chi/chi/v5"
)

type domainsHarness struct {
	*sitesHarness
	zone     *sitestest.Zone
	resolver *sitestest.Resolver
}

func newDomainsHarness(t *testing.T, developer func(http.Handler) http.Handler) *domainsHarness {
	return newDomainsHarnessWith(t, developer, nil, 0)
}

func newDomainsHarnessWith(t *testing.T, developer, limiter func(http.Handler) http.Handler, maxOnZone int) *domainsHarness {
	t.Helper()
	publisher, origins, zone, resolver := sitestest.NewPublisher(), sitestest.NewOrigins(), sitestest.NewZone(), sitestest.NewResolver()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{
		BaseDomain: "excalibase.app", SpoolDir: t.TempDir(), Hostnames: sitestest.NewMemoryHostnames(),
		CustomDomains: &sites.CustomDomainOptions{Zone: zone, Target: "cname.excalibase.app", Resolver: resolver, MaxOnZone: maxOnZone},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	if limiter != nil {
		handler.SetDomainWriteLimiter(limiter)
	}
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}/sites", func(r chi.Router) { handler.Routes(r, developer) })
	return &domainsHarness{sitesHarness: &sitesHarness{router: router, publisher: publisher, origins: origins}, zone: zone, resolver: resolver}
}

// addProven adds the domain over the API, publishes its TXT proof and checks it.
func (h *domainsHarness) addProven(t *testing.T, siteID, hostname string) *httptest.ResponseRecorder {
	t.Helper()
	added := h.do(t, http.MethodPost, "/"+siteID+"/domains", []byte(`{"hostname":"`+hostname+`"}`))
	var claim sites.CustomHostname
	decodeSiteBody(t, added, &claim)
	h.resolver.Set(claim.Records[1].Name, claim.Records[1].Value)
	return h.do(t, http.MethodPost, "/"+siteID+"/domains/"+claim.ID+"/refresh", nil)
}

func TestSiteDomainsAddListRefreshRemove(t *testing.T) {
	h := newDomainsHarness(t, passThrough)
	site := h.createSite(t, "shop")
	added := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"www.example.com"}`))
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Body)
	}
	var hostname sites.CustomHostname
	decodeSiteBody(t, added, &hostname)
	if hostname.Status != sites.HostnamePending || hostname.VerifiedAt != nil || len(hostname.Records) != 2 || hostname.Records[0].Value != "cname.excalibase.app" ||
		hostname.Records[1].Purpose != sites.PurposeVerification {
		t.Fatalf("added = %+v", hostname)
	}
	if strings.Contains(added.Body.String(), "cloudflare") || strings.Contains(added.Body.String(), "verificationToken") {
		t.Errorf("the response names Cloudflare's own id: %s", added.Body)
	}
	listed := h.do(t, http.MethodGet, "/"+site.ID+"/domains", nil)
	var all []sites.CustomHostname
	decodeSiteBody(t, listed, &all)
	if listed.Code != http.StatusOK || len(all) != 1 || all[0].ID != hostname.ID {
		t.Fatalf("list: %d %s", listed.Code, listed.Body)
	}
	refreshed := h.do(t, http.MethodPost, "/"+site.ID+"/domains/"+hostname.ID+"/refresh", nil)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", refreshed.Code, refreshed.Body)
	}
	removed := h.do(t, http.MethodDelete, "/"+site.ID+"/domains/"+hostname.ID, nil)
	if removed.Code != http.StatusNoContent || h.zone.Holds("www.example.com") {
		t.Fatalf("remove: %d %s", removed.Code, removed.Body)
	}
}

func TestSiteDomainRefusals(t *testing.T) {
	h := newDomainsHarness(t, passThrough)
	site := h.createSite(t, "shop")
	for body, want := range map[string]int{
		`{"hostname":"10.0.0.1"}`:                         http.StatusBadRequest,
		`{"hostname":"api.excalibase.io"}`:                http.StatusBadRequest,
		`{"hostname":"www.example.com","validation":"x"}`: http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
	} {
		if response := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(body)); response.Code != want {
			t.Errorf("%s = %d %s, want %d", body, response.Code, response.Body, want)
		}
	}
	h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"a.example.com"}`))
	h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"b.example.com"}`))
	if response := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"c.example.com"}`)); response.Code != http.StatusConflict {
		t.Errorf("past the limit = %d %s", response.Code, response.Body)
	}
	if response := h.do(t, http.MethodPost, "/"+site.ID+"/domains/not-a-hostname-id/refresh", nil); response.Code != http.StatusNotFound {
		t.Errorf("unknown hostname = %d", response.Code)
	}
	if response := h.do(t, http.MethodDelete, "/"+site.ID+"/domains/bad%20id", nil); response.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", response.Code)
	}
}

// Reading is for every member; adding, checking and removing a domain is for developers.
func TestSiteDomainWritesNeedTheDeveloperGate(t *testing.T) {
	refuse := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	}
	h := newDomainsHarness(t, refuse)
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/s1/domains"}, {http.MethodPost, "/s1/domains/d1/refresh"}, {http.MethodDelete, "/s1/domains/d1"},
	} {
		if response := h.do(t, call.method, call.path, []byte(`{}`)); response.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want the gate", call.method, call.path, response.Code)
		}
	}
	if response := h.do(t, http.MethodGet, "/s1/domains", nil); response.Code == http.StatusForbidden {
		t.Errorf("reading the list was gated")
	}
}

func TestSiteDomainsWithoutAZoneSayCustomDomainsAreNotAvailable(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	response := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"www.example.com"}`))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "custom domains are not available") {
		t.Fatalf("add = %d %s", response.Code, response.Body)
	}
	if listed := h.do(t, http.MethodGet, "/"+site.ID+"/domains", nil); listed.Code != http.StatusOK {
		t.Fatalf("list = %d %s", listed.Code, listed.Body)
	}
}

func TestSiteDomainCDNFailureIsABadGatewayWithoutDetail(t *testing.T) {
	h := newDomainsHarness(t, passThrough)
	site := h.createSite(t, "shop")
	h.zone.FailCreate = context.DeadlineExceeded
	response := h.addProven(t, site.ID, "www.example.com")
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "check the custom domain") {
		t.Fatalf("check = %d %s", response.Code, response.Body)
	}
}

// A full zone is a conflict that says so, not a missing feature.
func TestAFullZoneIsAConflictThatSaysSo(t *testing.T) {
	h := newDomainsHarnessWith(t, passThrough, nil, 1)
	site := h.createSite(t, "shop")
	if proven := h.addProven(t, site.ID, "www.example.com"); proven.Code != http.StatusOK {
		t.Fatalf("check = %d %s", proven.Code, proven.Body)
	}
	response := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"blog.example.com"}`))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "no room for another custom domain") {
		t.Fatalf("add = %d %s", response.Code, response.Body)
	}
}

// Adding and removing are rate limited; reading and checking are not.
func TestSiteDomainWritesGoThroughTheLimiter(t *testing.T) {
	limited := []string{}
	limiter := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limited = append(limited, r.Method)
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	h := newDomainsHarnessWith(t, passThrough, limiter, 0)
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/s1/domains"}, {http.MethodDelete, "/s1/domains/d1"},
		{http.MethodGet, "/s1/domains"}, {http.MethodPost, "/s1/domains/d1/refresh"},
	} {
		h.do(t, call.method, call.path, []byte(`{}`))
	}
	if strings.Join(limited, ",") != "POST,DELETE" {
		t.Fatalf("limited = %v, want the add and the remove only", limited)
	}
}
