package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	testOAuthProject   = "proj_oauth1"
	testOAuthOrgSlug   = "acme"
	testOAuthBaseURL   = "https://api.example.com/"
	testOAuthProviders = "/api/projects/" + testOAuthProject + "/auth-providers"
	testOAuthGitHub    = testOAuthProviders + "/github"
	testOAuthSecret    = "gh-secret-value"
	testOAuthCallback  = "https://api.example.com/auth/acme/proj_oauth1/callback/github"
	testOAuthSecretAt  = "projects/proj_oauth1/auth/providers/github"
)

// memAuthProviderStore is the in-memory ProjectAuthProviderStore.
type memAuthProviderStore struct {
	rows map[string]domain.ProjectAuthProvider
	err  error
}

func newMemAuthProviderStore() *memAuthProviderStore {
	return &memAuthProviderStore{rows: map[string]domain.ProjectAuthProvider{}}
}

func (m *memAuthProviderStore) ListAuthProviders(_ context.Context, projectID string) ([]domain.ProjectAuthProvider, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := []domain.ProjectAuthProvider{}
	for key, row := range m.rows {
		if strings.HasPrefix(key, projectID+"/") {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memAuthProviderStore) PutAuthProvider(_ context.Context, projectID string, provider domain.ProjectAuthProvider) error {
	if m.err != nil {
		return m.err
	}
	m.rows[projectID+"/"+provider.Provider] = provider
	return nil
}

// memAuthSignInStore keeps the policy and writes the settings through to the
// settings fake, as the one Postgres row does.
type memAuthSignInStore struct {
	settings *memAuthSettingsStore
	signIn   map[string]domain.ProjectAuthSignIn
	err      error
}

func (m *memAuthSignInStore) GetAuthSignIn(_ context.Context, projectID string) (domain.ProjectAuthSignIn, error) {
	if m.err != nil {
		return domain.ProjectAuthSignIn{}, m.err
	}
	if got, ok := m.signIn[projectID]; ok {
		return got, nil
	}
	return domain.DefaultAuthSignIn(), nil
}

func (m *memAuthSignInStore) SetAuthSettingsWithSignIn(_ context.Context, projectID string, settings domain.ProjectAuthSettings, signIn domain.ProjectAuthSignIn) error {
	if m.err != nil {
		return m.err
	}
	m.settings.settings[projectID] = settings
	m.signIn[projectID] = signIn
	return nil
}

func (m *memAuthSignInStore) SetGuestCaptchaSecretSet(_ context.Context, projectID string, set bool) error {
	if m.err != nil {
		return m.err
	}
	signIn, ok := m.signIn[projectID]
	if !ok {
		signIn = domain.DefaultAuthSignIn()
	}
	signIn.GuestCaptchaSecretSet = set
	m.signIn[projectID] = signIn
	return nil
}

func (m *memAuthProviderStore) DeleteAuthProvider(_ context.Context, projectID, provider string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.rows, projectID+"/"+provider)
	return nil
}

type failingPutSecrets struct{ *fakeVault }

func (failingPutSecrets) Put(string, map[string]string) error { return errors.New("vault sealed") }
func (failingPutSecrets) PutIfAbsent(string, map[string]string) (bool, error) {
	return false, errors.New("vault sealed")
}

type oauthFixture struct {
	router    *chi.Mux
	handler   *ProvisioningHandler
	settings  *memAuthSettingsStore
	signIn    *memAuthSignInStore
	providers *memAuthProviderStore
	vault     *fakeVault
	audit     *recordingAudit
}

// setupOAuthHandler mounts the provider, auth-settings and /info routes on a
// handler whose project belongs to org "acme"; flagOn decides authoauth.
func setupOAuthHandler(t *testing.T, flagOn bool) oauthFixture {
	t.Helper()
	if flagOn {
		return setupSignInHandler(t, features.AuthOAuth)
	}
	return setupSignInHandler(t)
}

// setupSignInHandler is setupOAuthHandler with the given sign-in flags on.
func setupSignInHandler(t *testing.T, on ...features.Feature) oauthFixture {
	t.Helper()
	fsStore, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := fsStore.Create(&domain.DatabaseInstance{ProjectID: testOAuthProject, OrgID: "org-oauth", Status: "ACTIVE"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := service.NewProvisioningService(fsStore, provisioner.NewFactory(), nil)
	h := NewProvisioningHandler(svc, &adminOrgStore{org: &domain.Org{ID: "org-oauth", Slug: testOAuthOrgSlug, Name: "Acme"}})
	f := oauthFixture{handler: h, vault: newFakeVault(), audit: &recordingAudit{}, providers: newMemAuthProviderStore(),
		settings: &memAuthSettingsStore{settings: map[string]domain.ProjectAuthSettings{}}}
	f.signIn = &memAuthSignInStore{settings: f.settings, signIn: map[string]domain.ProjectAuthSignIn{}}
	h.SetAuthSettingsStore(f.settings)
	h.SetAuthOAuth(AuthOAuthDeps{Flags: features.NewStatic(on...), Providers: f.providers, SignIn: f.signIn, Secrets: f.vault,
		PublicBaseURL: testOAuthBaseURL, Audit: f.audit})

	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/info", h.GetProjectInfo)
	r.Route("/api/projects/{projectId}/auth-settings", func(r chi.Router) {
		r.Get("/", h.GetAuthSettings)
		r.Put("/", h.PutAuthSettings)
	})
	r.Route("/api/projects/{projectId}/auth-providers", func(r chi.Router) {
		r.Get("/", h.GetAuthProviders)
		r.Put("/{provider}", h.PutAuthProvider)
		r.Delete("/{provider}", h.DeleteAuthProvider)
	})
	f.router = r
	return f
}

type providersBody struct {
	Providers []map[string]any `json:"providers"`
}

func decodeProviders(t *testing.T, body []byte) providersBody {
	t.Helper()
	var out providersBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode providers: %v (%s)", err, body)
	}
	return out
}

func TestAuthProviders_ListsBothProvidersUnconfigured(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodGet, testOAuthProviders, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	got := decodeProviders(t, w.Body.Bytes()).Providers
	if len(got) != 2 || got[0]["provider"] != "google" || got[1]["provider"] != "github" {
		t.Fatalf("providers = %v", got)
	}
	if got[1]["enabled"] != false || got[1]["clientId"] != "" || got[1]["clientSecretSet"] != false || got[1]["callbackUrl"] != testOAuthCallback {
		t.Fatalf("github = %v", got[1])
	}
	if len(got[0]) != 5 {
		t.Fatalf("a provider carries exactly five fields: %v", got[0])
	}
}

func TestAuthProviders_ListNeverCarriesTheSecret(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh-id", ClientSecretSet: true}
	f.vault.data[testOAuthSecretAt] = map[string]string{"client_secret": testOAuthSecret}
	w := doJSON(f.router, http.MethodGet, testOAuthProviders, nil)
	if strings.Contains(w.Body.String(), testOAuthSecret) {
		t.Fatalf("the secret leaked: %s", w.Body.String())
	}
	github := decodeProviders(t, w.Body.Bytes()).Providers[1]
	if github["enabled"] != true || github["clientId"] != "gh-id" || github["clientSecretSet"] != true {
		t.Fatalf("github = %v", github)
	}
}

func TestAuthProviders_PutWritesTheSecretToTheVaultThenTheRow(t *testing.T) {
	f := setupOAuthHandler(t, true)
	w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"enabled": true, "clientId": " gh-id ", "clientSecret": testOAuthSecret})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := f.vault.data[testOAuthSecretAt]; len(got) != 1 || got["client_secret"] != testOAuthSecret {
		t.Fatalf("vault = %v", f.vault.data)
	}
	row := f.providers.rows[testOAuthProject+"/github"]
	if !row.Enabled || row.ClientID != "gh-id" || !row.ClientSecretSet {
		t.Fatalf("row = %+v", row)
	}
	if strings.Contains(w.Body.String(), testOAuthSecret) || !strings.Contains(w.Body.String(), `"clientSecretSet":true`) ||
		!strings.Contains(w.Body.String(), testOAuthCallback) {
		t.Fatalf("response = %s", w.Body.String())
	}
}

func TestAuthProviders_PutWithoutASecretKeepsTheStoredOne(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", ClientID: "gh-id", ClientSecretSet: true}
	f.vault.data[testOAuthSecretAt] = map[string]string{"client_secret": testOAuthSecret}
	for _, body := range []map[string]any{
		{"enabled": true, "clientId": "gh-id-2"},
		{"enabled": true, "clientId": "gh-id-2", "clientSecret": ""},
	} {
		w := doJSON(f.router, http.MethodPut, testOAuthGitHub, body)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if f.vault.data[testOAuthSecretAt]["client_secret"] != testOAuthSecret {
		t.Fatal("the stored secret changed")
	}
	if row := f.providers.rows[testOAuthProject+"/github"]; !row.ClientSecretSet || row.ClientID != "gh-id-2" || !row.Enabled {
		t.Fatalf("row = %+v", row)
	}
}

func TestAuthProviders_PutRefusals(t *testing.T) {
	cases := map[string]struct {
		path string
		body map[string]any
		code int
	}{
		"unknown provider":          {testOAuthProviders + "/apple", map[string]any{"clientId": "x"}, http.StatusNotFound},
		"enabled without client id": {testOAuthGitHub, map[string]any{"enabled": true, "clientSecret": "s"}, http.StatusBadRequest},
		"enabled without a secret":  {testOAuthGitHub, map[string]any{"enabled": true, "clientId": "gh"}, http.StatusBadRequest},
		"client id with a space":    {testOAuthGitHub, map[string]any{"clientId": "gh id"}, http.StatusBadRequest},
		"client id too long":        {testOAuthGitHub, map[string]any{"clientId": strings.Repeat("a", 257)}, http.StatusBadRequest},
		"secret with a newline":     {testOAuthGitHub, map[string]any{"clientId": "gh", "clientSecret": "s\n"}, http.StatusBadRequest},
		"secret too long":           {testOAuthGitHub, map[string]any{"clientId": "gh", "clientSecret": strings.Repeat("s", 513)}, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupOAuthHandler(t, true)
			w := doJSON(f.router, http.MethodPut, tc.path, tc.body)
			if w.Code != tc.code {
				t.Fatalf("got %d want %d (%s)", w.Code, tc.code, w.Body.String())
			}
			if len(f.vault.data) != 0 || len(f.providers.rows) != 0 {
				t.Fatalf("a refused PUT wrote: vault=%v rows=%v", f.vault.data, f.providers.rows)
			}
		})
	}
}

func TestAuthProviders_InvalidBodyAndProject(t *testing.T) {
	f := setupOAuthHandler(t, true)
	if w := doJSON(f.router, http.MethodPut, testOAuthGitHub, "not an object"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodGet, "/api/projects/bad..id/auth-providers", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad project GET: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, "/api/projects/bad..id/auth-providers/github", map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad project PUT: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodGet, "/api/projects/proj_missing/auth-providers", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", w.Code)
	}
}

func TestAuthProviders_AVaultFailureLeavesNoRowClaimingASecret(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.handler.authOAuth.Secrets = failingPutSecrets{f.vault}
	w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"enabled": true, "clientId": "gh", "clientSecret": testOAuthSecret})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d (%s)", w.Code, w.Body.String())
	}
	if len(f.providers.rows) != 0 {
		t.Fatalf("a row was written although the vault refused the secret: %v", f.providers.rows)
	}
}

func TestAuthProviders_Unavailable(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.handler.authOAuth.Secrets = nil
	if w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"clientId": "gh", "clientSecret": "s"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no vault, secret given: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"clientId": "gh"}); w.Code != http.StatusOK {
		t.Fatalf("no vault, no secret: %d %s", w.Code, w.Body.String())
	}
	f.handler.authOAuth.Providers = nil
	if w := doJSON(f.router, http.MethodGet, testOAuthProviders, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store GET: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"clientId": "gh"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store PUT: %d", w.Code)
	}
}

func TestAuthProviders_StoreFailureIs500(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.providers.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodGet, testOAuthProviders, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"clientId": "gh"}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT: %d", w.Code)
	}
}

func TestAuthProviders_PutIsAuditedWithoutTheSecret(t *testing.T) {
	f := setupOAuthHandler(t, true)
	doJSON(f.router, http.MethodPut, testOAuthGitHub, map[string]any{"enabled": true, "clientId": "gh", "clientSecret": testOAuthSecret})
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit entries = %d", len(f.audit.entries))
	}
	entry := f.audit.entries[0]
	if entry.Action != "auth_provider.update" || entry.ProjectID != testOAuthProject || entry.ResourceID != "github" ||
		!strings.Contains(entry.Details, `"secretChanged":true`) || strings.Contains(entry.Details, testOAuthSecret) {
		t.Fatalf("audit = %+v", entry)
	}
}

type failingDeleteSecrets struct{ *fakeVault }

func (failingDeleteSecrets) Delete(string) error { return errors.New("vault sealed") }

func TestAuthProviders_DeleteRemovesTheSecretAndTheRow(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh-id", ClientSecretSet: true}
	f.vault.data[testOAuthSecretAt] = map[string]string{"client_secret": testOAuthSecret}
	f.vault.data["projects/proj_oauth1/auth/providers/google"] = map[string]string{"client_secret": "keep"}
	w := doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	if _, ok := f.vault.data[testOAuthSecretAt]; ok {
		t.Fatal("the secret is still in the vault")
	}
	if _, ok := f.vault.data["projects/proj_oauth1/auth/providers/google"]; !ok {
		t.Fatal("another provider's secret went too")
	}
	if _, ok := f.providers.rows[testOAuthProject+"/github"]; ok {
		t.Fatal("the row survived")
	}
	if !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"clientSecretSet":false`) {
		t.Fatalf("response = %s", w.Body.String())
	}
	if again := doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil); again.Code != http.StatusOK {
		t.Fatalf("a second DELETE: %d", again.Code)
	}
}

func TestAuthProviders_DeleteIsAudited(t *testing.T) {
	f := setupOAuthHandler(t, true)
	f.vault.data[testOAuthSecretAt] = map[string]string{"client_secret": testOAuthSecret}
	doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil)
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit entries = %d", len(f.audit.entries))
	}
	entry := f.audit.entries[0]
	if entry.Action != "auth_provider.delete" || entry.ProjectID != testOAuthProject || entry.ResourceID != "github" ||
		entry.Resource != "auth_provider" || strings.Contains(entry.Details, testOAuthSecret) {
		t.Fatalf("audit = %+v", entry)
	}
}

func TestAuthProviders_DeleteRefusals(t *testing.T) {
	f := setupOAuthHandler(t, true)
	if w := doJSON(f.router, http.MethodDelete, testOAuthProviders+"/apple", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodDelete, "/api/projects/bad..id/auth-providers/github", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad project: %d", w.Code)
	}
	f.providers.rows[testOAuthProject+"/github"] = domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh", ClientSecretSet: true}
	f.vault.data[testOAuthSecretAt] = map[string]string{"client_secret": testOAuthSecret}
	f.handler.authOAuth.Secrets = failingDeleteSecrets{f.vault}
	if w := doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("vault refused: %d", w.Code)
	}
	if _, ok := f.providers.rows[testOAuthProject+"/github"]; ok {
		t.Fatal("the provider stayed on although its secret is going")
	}
	f.handler.authOAuth.Secrets = nil
	if w := doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no vault: %d", w.Code)
	}
	f.handler.authOAuth.Secrets = f.vault
	f.providers.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, http.MethodDelete, testOAuthGitHub, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: %d", w.Code)
	}
	if len(f.audit.entries) != 0 {
		t.Fatalf("a failed DELETE was audited: %+v", f.audit.entries)
	}
}
