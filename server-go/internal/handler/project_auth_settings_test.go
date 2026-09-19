package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	testAuthProject = "proj_auth1"
	testAuthPath    = "/api/projects/" + testAuthProject + "/auth-settings"
	testAuthSiteURL = "https://app.example.com"
)

// memAuthSettingsStore is the in-memory ProjectAuthSettingsStore for handler tests.
type memAuthSettingsStore struct {
	settings map[string]domain.ProjectAuthSettings
	err      error
}

func (m *memAuthSettingsStore) GetAuthSettings(_ context.Context, projectID string) (domain.ProjectAuthSettings, bool, error) {
	if m.err != nil {
		return domain.ProjectAuthSettings{}, false, m.err
	}
	if s, ok := m.settings[projectID]; ok {
		return s, true, nil
	}
	return domain.ProjectAuthSettings{}, false, nil
}

func (m *memAuthSettingsStore) SetAuthSettings(_ context.Context, projectID string, settings domain.ProjectAuthSettings) error {
	if m.err != nil {
		return m.err
	}
	m.settings[projectID] = settings
	return nil
}

type authSettingsFixture struct {
	router  *chi.Mux
	handler *ProvisioningHandler
	store   *memAuthSettingsStore
}

func setupAuthSettingsHandler(t *testing.T) authSettingsFixture {
	t.Helper()
	fsStore, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	svc := service.NewProvisioningService(fsStore, provisioner.NewFactory(), nil)
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	authStore := &memAuthSettingsStore{settings: map[string]domain.ProjectAuthSettings{}}
	h.SetAuthSettingsStore(authStore)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/auth-settings", func(r chi.Router) {
		r.Get("/", h.GetAuthSettings)
		r.Put("/", h.PutAuthSettings)
	})
	return authSettingsFixture{router: r, handler: h, store: authStore}
}

func decodeAuthSettings(t *testing.T, body []byte) authSettingsResponse {
	t.Helper()
	var out authSettingsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode auth settings response: %v (%s)", err, body)
	}
	return out
}

func TestAuthSettings_DefaultIsUnset(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	w := doJSON(f.router, "GET", testAuthPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	got := decodeAuthSettings(t, w.Body.Bytes())
	if got.RequireEmailVerification || got.SiteURL != "" {
		t.Fatalf("new project must default to unset, got %+v", got)
	}
}

func TestAuthSettings_PutRoundTrip(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	body := map[string]interface{}{"requireEmailVerification": true, "siteUrl": testAuthSiteURL}
	w := doJSON(f.router, "PUT", testAuthPath, body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := decodeAuthSettings(t, w.Body.Bytes())
	if !got.RequireEmailVerification || got.SiteURL != testAuthSiteURL {
		t.Fatalf("PUT response = %+v", got)
	}
	if stored := f.store.settings[testAuthProject]; !stored.RequireEmailVerification || stored.SiteURL != testAuthSiteURL {
		t.Fatalf("store = %+v", stored)
	}

	w = doJSON(f.router, "GET", testAuthPath, nil)
	if got := decodeAuthSettings(t, w.Body.Bytes()); !got.RequireEmailVerification || got.SiteURL != testAuthSiteURL {
		t.Fatalf("GET after PUT = %+v", got)
	}
}

func TestAuthSettings_PutCanonicalisesSiteURL(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	body := map[string]interface{}{"requireEmailVerification": false, "siteUrl": " HTTPS://App.Example.com "}
	w := doJSON(f.router, "PUT", testAuthPath, body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := decodeAuthSettings(t, w.Body.Bytes())
	if got.SiteURL != testAuthSiteURL {
		t.Fatalf("siteUrl not canonicalised: got %q", got.SiteURL)
	}
}

func TestAuthSettings_PutEmptySiteURLClears(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	f.store.settings[testAuthProject] = domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: testAuthSiteURL}
	w := doJSON(f.router, "PUT", testAuthPath, map[string]interface{}{"requireEmailVerification": false, "siteUrl": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := decodeAuthSettings(t, w.Body.Bytes())
	if got.SiteURL != "" || got.RequireEmailVerification {
		t.Fatalf("PUT with empty siteUrl must clear it, got %+v", got)
	}
}

func TestAuthSettings_PutInvalidSiteURLIs400AndNamesField(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	cases := []string{
		"app.example.com",             // no scheme
		"https://",                    // no host
		"https://app.example.com/",    // trailing slash
		"https://u:p@app.example.com", // userinfo
	}
	for _, siteURL := range cases {
		t.Run(siteURL, func(t *testing.T) {
			w := doJSON(f.router, "PUT", testAuthPath, map[string]interface{}{"requireEmailVerification": false, "siteUrl": siteURL})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("siteUrl %q: got %d want 400 (%s)", siteURL, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "siteUrl") && !strings.Contains(strings.ToLower(w.Body.String()), "site url") {
				t.Fatalf("400 body must name siteUrl: %s", w.Body.String())
			}
		})
	}
}

func TestAuthSettings_RejectedPutLeavesStoreUntouched(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	f.store.settings[testAuthProject] = domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: testAuthSiteURL}
	w := doJSON(f.router, "PUT", testAuthPath, map[string]interface{}{"requireEmailVerification": false, "siteUrl": "not-a-url"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	got := f.store.settings[testAuthProject]
	if !got.RequireEmailVerification || got.SiteURL != testAuthSiteURL {
		t.Fatalf("store changed on a rejected PUT: %+v", got)
	}
}

func TestAuthSettings_InvalidProjectIDIsRejected(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	if w := doJSON(f.router, "GET", "/api/projects/bad..id/auth-settings", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("GET bad id: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", "/api/projects/bad..id/auth-settings", map[string]interface{}{}); w.Code != http.StatusBadRequest {
		t.Fatalf("PUT bad id: %d", w.Code)
	}
}

func TestAuthSettings_StoreErrorIs500(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	f.store.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, "GET", testAuthPath, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET with failing store: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", testAuthPath, map[string]interface{}{"siteUrl": testAuthSiteURL}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT with failing store: %d", w.Code)
	}
}

func TestAuthSettings_WithoutStoreIsUnavailable(t *testing.T) {
	f := setupAuthSettingsHandler(t)
	f.handler.SetAuthSettingsStore(nil)
	if w := doJSON(f.router, "GET", testAuthPath, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET without store: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", testAuthPath, map[string]interface{}{"siteUrl": testAuthSiteURL}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without store: %d", w.Code)
	}
}

// setupAuthSettingsInfoRouter mounts /info on a handler whose org store
// resolves the project's org, so the auth-settings branch of GetProjectInfo
// is reachable.
func setupAuthSettingsInfoRouter(t *testing.T) (*chi.Mux, *ProvisioningHandler, *memAuthSettingsStore) {
	t.Helper()
	fsStore, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := fsStore.Create(&domain.DatabaseInstance{ProjectID: testAuthProject, ProjectName: "web", OrgID: "org-auth", Status: "ACTIVE"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc := service.NewProvisioningService(fsStore, provisioner.NewFactory(), nil)
	h := NewProvisioningHandler(svc, &adminOrgStore{org: &domain.Org{ID: "org-auth", Slug: "auth", Name: "Auth"}})
	authStore := &memAuthSettingsStore{settings: map[string]domain.ProjectAuthSettings{}}
	h.SetAuthSettingsStore(authStore)
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/info", h.GetProjectInfo)
	return r, h, authStore
}

func TestAuthSettings_InfoExposesFieldsAndDefaultsToUnset(t *testing.T) {
	r, h, authStore := setupAuthSettingsInfoRouter(t)
	authStore.settings[testAuthProject] = domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: testAuthSiteURL}
	w := doJSON(r, "GET", "/api/projects/"+testAuthProject+"/info", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	var info domain.ProjectInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !info.RequireEmailVerification || info.SiteURL != testAuthSiteURL {
		t.Fatalf("info auth fields = %+v", info)
	}

	h.SetAuthSettingsStore(nil)
	w = doJSON(r, "GET", "/api/projects/"+testAuthProject+"/info", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info without a store: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"requireEmailVerification":false`) || !strings.Contains(w.Body.String(), `"siteUrl":""`) {
		t.Fatalf("info without an auth settings store must report the zero value: %s", w.Body.String())
	}
}

func TestAuthSettings_InfoIs503WhenUnreadable(t *testing.T) {
	r, _, authStore := setupAuthSettingsInfoRouter(t)
	authStore.err = errors.New("pq: connection reset")
	w := doJSON(r, "GET", "/api/projects/"+testAuthProject+"/info", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("info with failing auth settings store must be 503 (auth service keeps last-good), got %d", w.Code)
	}
}
