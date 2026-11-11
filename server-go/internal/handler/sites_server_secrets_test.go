package handler

import (
	"fmt"
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

func newManySecretsHarness(t *testing.T, count int) (*sitesHarness, []string) {
	t.Helper()
	store := sitestest.NewMemoryStore()
	values, names := map[string]string{}, []string{}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s_%03d", strings.Repeat("S", 59), i)
		values[name] = "v"
		names = append(names, name)
	}
	service, err := sites.NewService(store, sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir(),
		Flags: features.NewStatic(features.SitesServer), Code: store, OutboundWorker: "excalibase-sites-outbound",
		Outbound: &sitestest.Outbound{Worker: "excalibase-sites-outbound"}, Secrets: sitestest.NewSecrets(values)})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &sitesHarness{router: sitesRouter(NewSiteHandler(service))}, names
}

// 100 secrets of 64-character names is a list of about 6.7 KB: bigger than the 4 KB a site body may be.
func TestSites_ServerSecretsListOfAHundredLongNamesIsAccepted(t *testing.T) {
	h, names := newManySecretsHarness(t, 100)
	body := `{"serverVisible":["` + strings.Join(names, `","`) + `"]}`
	if len(body) <= 4*1024 {
		t.Fatalf("the body is %d bytes, not past the site body cap", len(body))
	}
	if put := h.do(t, http.MethodPut, "/server-secrets", []byte(body)); put.Code != http.StatusOK {
		t.Fatalf("put = %d %.200s", put.Code, put.Body)
	}
}

func TestSites_ServerSecretsBodyPastTheCapIs413NotInvalidJSON(t *testing.T) {
	h, _ := newManySecretsHarness(t, 1)
	body := `{"serverVisible":["` + strings.Repeat("A", 200*1024) + `"]}`
	if put := h.do(t, http.MethodPut, "/server-secrets", []byte(body)); put.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("put = %d %.200s, want 413", put.Code, put.Body)
	}
}
