package handler

import (
	"errors"
	"net/http/httptest"
	"time"

	"context"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

type issuedDeployToken struct {
	sites.DeployToken
	Token string `json:"token"`
}

// A deploy token is shown once, on create; the list never carries it (EXC-620).
func TestSiteDeployTokensAreShownOnceListedAndRevoked(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	created := h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"github actions"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var issued issuedDeployToken
	decodeSiteBody(t, created, &issued)
	if !strings.HasPrefix(issued.Token, sites.DeployTokenPrefix) || issued.Name != "github actions" || issued.SiteID != site.ID || issued.ID == "" {
		t.Fatalf("issued = %+v", issued)
	}
	if strings.Contains(created.Body.String(), "hash") {
		t.Fatalf("create answer carries the hash: %s", created.Body)
	}

	listed := h.do(t, http.MethodGet, "/"+site.ID+"/tokens", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), issued.ID) || strings.Contains(listed.Body.String(), issued.Token) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body)
	}

	if revoked := h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil); revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", revoked.Code, revoked.Body)
	}
	if again := h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil); again.Code != http.StatusNotFound {
		t.Fatalf("second revoke = %d %s", again.Code, again.Body)
	}
}

func TestSiteDeployTokenRefusals(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	for name, call := range map[string]struct {
		method, path, body string
		want               int
	}{
		"bad json":     {http.MethodPost, "/" + site.ID + "/tokens", `{`, http.StatusBadRequest},
		"empty name":   {http.MethodPost, "/" + site.ID + "/tokens", `{"name":" "}`, http.StatusBadRequest},
		"missing site": {http.MethodPost, "/missing-site/tokens", `{"name":"ci"}`, http.StatusNotFound},
		"list missing": {http.MethodGet, "/missing-site/tokens", ``, http.StatusNotFound},
		"bad token id": {http.MethodDelete, "/" + site.ID + "/tokens/bad%20id", ``, http.StatusBadRequest},
	} {
		if got := h.do(t, call.method, call.path, []byte(call.body)); got.Code != call.want {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body, call.want)
		}
	}
	for range sites.MaxDeployTokensPerSite {
		h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`))
	}
	if over := h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`)); over.Code != http.StatusConflict {
		t.Fatalf("one past the cap = %d %s", over.Code, over.Body)
	}
}

type siteTokenAudit struct{ entries []*domain.AuditEntry }

func (a *siteTokenAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

// Making and revoking a deploy token is audited by prefix, never the token.
func TestSiteDeployTokensAreAudited(t *testing.T) {
	publisher, origins := sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	audit := &siteTokenAudit{}
	handler.SetAuditLog(audit)
	h := &sitesHarness{router: sitesRouter(handler)}
	site := h.createSite(t, "shop")
	var issued issuedDeployToken
	decodeSiteBody(t, h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`)), &issued)
	h.do(t, http.MethodDelete, "/"+site.ID+"/tokens/"+issued.ID, nil)
	if len(audit.entries) != 2 || audit.entries[0].Action != "site_deploy_token.create" || audit.entries[1].Action != "site_deploy_token.revoke" {
		t.Fatalf("audit = %+v", audit.entries)
	}
	for _, entry := range audit.entries {
		if entry.ProjectID != sitesTestProject || entry.ResourceID != issued.ID || strings.Contains(entry.Details, issued.Token) || !strings.Contains(entry.Details, site.ID) {
			t.Errorf("entry = %+v", entry)
		}
	}
}

func createTokenAs(t *testing.T, h *sitesHarness, siteID, body string, caller *domain.AccessToken) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+sitesTestProject+"/sites/"+siteID+"/tokens", strings.NewReader(body))
	if caller != nil {
		request = request.WithContext(auth.SetToken(request.Context(), caller))
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

// The expiry is chosen at creation, 90 days unless said, a year at most.
func TestSiteDeployTokenExpiryIsChosenAtCreation(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	for body, days := range map[string]int{`{"name":"ci"}`: 90, `{"name":"ci","expiresInDays":30}`: 30, `{"name":"ci","expiresInDays":365}`: 365} {
		response := createTokenAs(t, h, site.ID, body, nil)
		var issued issuedDeployToken
		decodeSiteBody(t, response, &issued)
		want := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		if response.Code != http.StatusCreated || issued.ExpiresAt.Sub(want).Abs() > time.Minute {
			t.Errorf("%s: %d expires %v, want about %v", body, response.Code, issued.ExpiresAt, want)
		}
	}
	for _, body := range []string{`{"name":"ci","expiresInDays":366}`, `{"name":"ci","expiresInDays":-1}`} {
		if response := createTokenAs(t, h, site.ID, body, nil); response.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, response.Code, response.Body)
		}
	}
}

// Made through a personal access token, it never outlives that token; a
// Studio session's own expiry does not shorten it.
func TestSiteDeployTokenNeverOutlivesTheCallersPAT(t *testing.T) {
	h := newSitesHarness(t)
	site := h.createSite(t, "shop")
	patExpiry := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	response := createTokenAs(t, h, site.ID, `{"name":"ci"}`, &domain.AccessToken{Scopes: auth.ScopeWrite, ExpiresAt: &patExpiry})
	var issued issuedDeployToken
	decodeSiteBody(t, response, &issued)
	if response.Code != http.StatusCreated || !issued.ExpiresAt.Equal(patExpiry) {
		t.Fatalf("%d expires %v, want the PAT's %v", response.Code, issued.ExpiresAt, patExpiry)
	}
	sessionExpiry := time.Now().Add(24 * time.Hour)
	response = createTokenAs(t, h, site.ID, `{"name":"ci"}`, &domain.AccessToken{Scopes: auth.ScopeSession, ExpiresAt: &sessionExpiry})
	decodeSiteBody(t, response, &issued)
	if response.Code != http.StatusCreated || issued.ExpiresAt.Before(time.Now().Add(89*24*time.Hour)) {
		t.Fatalf("a session's expiry shortened the token: %v", issued.ExpiresAt)
	}
}

type fixedStanding struct {
	canDeploy bool
	err       error
}

func (f fixedStanding) CreatorCanDeploy(context.Context, *sites.DeployToken) (bool, error) {
	return f.canDeploy, f.err
}

func listedTokens(t *testing.T, standing tokenStanding) (int, []listedDeployToken) {
	t.Helper()
	service, err := sites.NewService(sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins(), sites.Options{BaseDomain: "excalibase.app", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler := NewSiteHandler(service)
	handler.SetTokenStanding(standing)
	h := &sitesHarness{router: sitesRouter(handler)}
	site := h.createSite(t, "shop")
	h.do(t, http.MethodPost, "/"+site.ID+"/tokens", []byte(`{"name":"ci"}`))
	response := h.do(t, http.MethodGet, "/"+site.ID+"/tokens", nil)
	var listed []listedDeployToken
	if response.Code == http.StatusOK {
		decodeSiteBody(t, response, &listed)
	}
	return response.Code, listed
}

type listedDeployToken struct {
	sites.DeployToken
	CreatorCanDeploy bool `json:"creatorCanDeploy"`
}

// Each listed token says whether its creator may still deploy, so Studio can
// mark one inactive the way the deploy route would refuse it (EXC-620).
func TestSiteDeployTokensListSayWhetherTheirCreatorCanStillDeploy(t *testing.T) {
	for _, canDeploy := range []bool{true, false} {
		code, listed := listedTokens(t, fixedStanding{canDeploy: canDeploy})
		if code != http.StatusOK || len(listed) != 1 || listed[0].CreatorCanDeploy != canDeploy {
			t.Fatalf("standing %v: %d %+v", canDeploy, code, listed)
		}
	}
	if code, _ := listedTokens(t, fixedStanding{err: errors.New("database down")}); code != http.StatusInternalServerError {
		t.Fatalf("an unreadable standing answered %d", code)
	}
}
