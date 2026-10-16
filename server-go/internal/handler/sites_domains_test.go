package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
	"github.com/go-chi/chi/v5"
)

type domainsHarness struct {
	*sitesHarness
	zone *sitestest.Zone
}

func newDomainsHarness(t *testing.T, developer func(http.Handler) http.Handler) *domainsHarness {
	t.Helper()
	publisher, origins, zone := sitestest.NewPublisher(), sitestest.NewOrigins(), sitestest.NewZone()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{
		BaseDomain: "excalibase.app", SpoolDir: t.TempDir(), Hostnames: sitestest.NewMemoryHostnames(),
		CustomDomains: &sites.CustomDomainOptions{Zone: zone, Target: "cname.excalibase.app", Resolver: sitestest.NewResolver()},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}/sites", func(r chi.Router) { NewSiteHandler(service).Routes(r, developer) })
	return &domainsHarness{sitesHarness: &sitesHarness{router: router, publisher: publisher, origins: origins}, zone: zone}
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
	if hostname.Status != sites.HostnamePending || len(hostname.Records) != 2 || hostname.Records[0].Value != "cname.excalibase.app" ||
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
	response := h.do(t, http.MethodPost, "/"+site.ID+"/domains", []byte(`{"hostname":"www.example.com"}`))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "add the custom domain") {
		t.Fatalf("add = %d %s", response.Code, response.Body)
	}
}
