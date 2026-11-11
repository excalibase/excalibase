package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func newSecretsSitesHarness(t *testing.T, on bool) *sitesHarness {
	t.Helper()
	store := sitestest.NewMemoryStore()
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.SitesServer)
	}
	service, err := sites.NewService(store, sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir(),
		Flags: flags, Code: store, OutboundWorker: "excalibase-sites-outbound", Outbound: &sitestest.Outbound{Worker: "excalibase-sites-outbound"},
		Secrets: sitestest.NewSecrets(map[string]string{"STRIPE_KEY": "sk_live_secret", "DB_PASSWORD": "hunter2"})})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &sitesHarness{router: sitesRouter(NewSiteHandler(service))}
}

func TestSites_ServerSecretsAreMarkedByNameAndValuesNeverLeave(t *testing.T) {
	h := newSecretsSitesHarness(t, true)
	put := h.do(t, http.MethodPut, "/server-secrets", []byte(`{"serverVisible":["STRIPE_KEY"]}`))
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `{"name":"STRIPE_KEY","serverVisible":true}`) ||
		!strings.Contains(put.Body.String(), `{"name":"DB_PASSWORD","serverVisible":false}`) {
		t.Fatalf("put = %d %s", put.Code, put.Body)
	}
	got := h.do(t, http.MethodGet, "/server-secrets", nil)
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "sk_live_secret") || strings.Contains(got.Body.String(), "hunter2") {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if bad := h.do(t, http.MethodPut, "/server-secrets", []byte(`{"serverVisible":["NOPE"]}`)); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown secret = %d %s", bad.Code, bad.Body)
	}
	if bad := h.do(t, http.MethodPut, "/server-secrets", []byte(`{}`)); bad.Code != http.StatusBadRequest {
		t.Fatalf("no list = %d %s", bad.Code, bad.Body)
	}
}

func TestSites_ServerSecretsAnswerAsUnmountedWhileOff(t *testing.T) {
	h := newSecretsSitesHarness(t, false)
	response := h.do(t, http.MethodGet, "/server-secrets", nil)
	if response.Code != http.StatusNotFound || response.Body.String() != "404 page not found\n" {
		t.Fatalf("get = %d %q", response.Code, response.Body)
	}
}
