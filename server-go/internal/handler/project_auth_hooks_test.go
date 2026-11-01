package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

const (
	testHooks         = "/api/projects/" + testOAuthProject + "/auth-hooks"
	testHooksSecret   = testHooks + "/secret"
	testHookClaims    = testHooks + "/custom_access_token"
	testHooksSecretAt = "projects/" + testOAuthProject + "/auth/hooks"
	testHookURL       = "https://hooks.example.com/claims?token=receiver-token"
)

// memAuthHookStore is the in-memory ProjectAuthHookStore.
type memAuthHookStore struct {
	rows      map[string]domain.ProjectAuthHook
	secretSet map[string]bool
	err       error
	putErr    error
	markErr   error
	lockErr   error
	events    []string
	locked    bool
}

func (m *memAuthHookStore) LockAuthHookSecret(_ context.Context, projectID string) (func(), error) {
	if m.lockErr != nil {
		return nil, m.lockErr
	}
	if m.locked {
		return nil, errors.New("lock held twice")
	}
	m.locked = true
	m.events = append(m.events, "lock")
	return func() { m.locked = false; m.events = append(m.events, "unlock") }, nil
}

func newMemAuthHookStore() *memAuthHookStore {
	return &memAuthHookStore{rows: map[string]domain.ProjectAuthHook{}, secretSet: map[string]bool{}}
}

func (m *memAuthHookStore) ListAuthHooks(_ context.Context, projectID string) ([]domain.ProjectAuthHook, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := []domain.ProjectAuthHook{}
	for key, row := range m.rows {
		if strings.HasPrefix(key, projectID+"/") {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memAuthHookStore) PutAuthHook(_ context.Context, projectID string, hook domain.ProjectAuthHook) error {
	if m.putErr != nil {
		return m.putErr
	}
	if hook.Enabled && !m.secretSet[projectID] {
		return domain.ErrAuthHookNeedsSecret
	}
	m.rows[projectID+"/"+hook.Hook] = hook
	return nil
}

func (m *memAuthHookStore) AuthHookSecretSet(_ context.Context, projectID string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.secretSet[projectID], nil
}

func (m *memAuthHookStore) MarkAuthHookSecretSet(_ context.Context, projectID string) error {
	m.events = append(m.events, "mark")
	if m.markErr != nil {
		return m.markErr
	}
	m.secretSet[projectID] = true
	return nil
}

func (m *memAuthHookStore) ClearAuthHookSecret(_ context.Context, projectID string) error {
	m.events = append(m.events, "clear")
	if m.putErr != nil {
		return m.putErr
	}
	for key, row := range m.rows {
		if strings.HasPrefix(key, projectID+"/") {
			row.Enabled = false
			m.rows[key] = row
		}
	}
	delete(m.secretSet, projectID)
	return nil
}

type hooksFixture struct {
	oauthFixture
	hooks *memAuthHookStore
}

func setupHooksHandler(t *testing.T, on ...features.Feature) hooksFixture {
	t.Helper()
	f := hooksFixture{oauthFixture: setupSignInHandler(t, on...), hooks: newMemAuthHookStore()}
	f.handler.authOAuth.Hooks = f.hooks
	f.router.Route("/api/projects/{projectId}/auth-hooks", func(r chi.Router) {
		r.Get("/", f.handler.GetAuthHooks)
		r.Post("/secret", f.handler.RotateAuthHookSecret)
		r.Delete("/secret", f.handler.DeleteAuthHookSecret)
		r.Put("/{hook}", f.handler.PutAuthHook)
	})
	return f
}

type hooksBody struct {
	SecretSet bool             `json:"secretSet"`
	Hooks     []map[string]any `json:"hooks"`
}

func decodeHooks(t *testing.T, body []byte) hooksBody {
	t.Helper()
	var out hooksBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode hooks: %v (%s)", err, body)
	}
	return out
}

func TestAuthHooks_ListsAllThreeUnsetInOrder(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	w := doJSON(f.router, http.MethodGet, testHooks, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	got := decodeHooks(t, w.Body.Bytes())
	if got.SecretSet || len(got.Hooks) != 3 || got.Hooks[0]["hook"] != "custom_access_token" ||
		got.Hooks[1]["hook"] != "before_user_created" || got.Hooks[2]["hook"] != "send_email" {
		t.Fatalf("body = %s", w.Body.String())
	}
	if got.Hooks[0]["enabled"] != false || got.Hooks[0]["url"] != "" || len(got.Hooks[0]) != 3 {
		t.Fatalf("a hook carries exactly hook, enabled and url: %v", got.Hooks[0])
	}
}

func TestAuthHooks_AnswerAsUnmountedWhileTheFlagIsOff(t *testing.T) {
	f := setupHooksHandler(t, features.AuthOAuth, features.AuthOTP, features.AuthGuest, features.AuthUser)
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, testHooks}, {http.MethodPut, testHookClaims},
		{http.MethodPost, testHooksSecret}, {http.MethodDelete, testHooksSecret},
	} {
		if w := doJSON(f.router, call.method, call.path, map[string]any{}); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", call.method, call.path, w.Code)
		}
	}
	if len(f.vault.data) != 0 || len(f.audit.entries) != 0 {
		t.Fatal("something was written with the flag off")
	}
}

func TestAuthHooks_UnavailableWithoutTheStoreOrTheVault(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.handler.authOAuth.Hooks = nil
	if w := doJSON(f.router, http.MethodGet, testHooks, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: %d", w.Code)
	}
	f = setupHooksHandler(t, features.AuthHooks)
	f.handler.authOAuth.Secrets = nil
	if w := doJSON(f.router, http.MethodPost, testHooksSecret, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no vault: %d", w.Code)
	}
}

func TestAuthHooks_ReadFailureIsAnInternalError(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.err = errors.New("connection refused to 10.0.0.3")
	w := doJSON(f.router, http.MethodGet, testHooks, nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "10.0.0.3") {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthHooks_SecretIsShownOnceStoredAndNeverReadBack(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	w := doJSON(f.router, http.MethodPost, testHooksSecret, nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	body := decodeObject(t, w.Body.Bytes())
	secret, _ := body["secret"].(string)
	if len(body) != 1 || !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("body = %s", w.Body.String())
	}
	if stored := f.vault.data[testHooksSecretAt]; len(stored) != 1 || stored["secret"] != secret {
		t.Fatalf("vault = %v", stored)
	}
	if len(f.audit.entries) != 1 || f.audit.entries[0].Action != "auth_hook_secret.rotate" ||
		strings.Contains(f.audit.entries[0].Details, secret) {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
	listed := doJSON(f.router, http.MethodGet, testHooks, nil)
	if !decodeHooks(t, listed.Body.Bytes()).SecretSet || strings.Contains(listed.Body.String(), secret) {
		t.Fatalf("GET after POST: %s", listed.Body.String())
	}

	again := decodeObject(t, doJSON(f.router, http.MethodPost, testHooksSecret, nil).Body.Bytes())
	if again["secret"] == secret || f.vault.data[testHooksSecretAt]["secret"] != again["secret"] {
		t.Fatal("rotating did not replace the secret at once")
	}
}

func TestAuthHooks_SecretIsNotMarkedSetWhenTheVaultRefuses(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.handler.authOAuth.Secrets = failingPutSecrets{f.vault}
	w := doJSON(f.router, http.MethodPost, testHooksSecret, nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "sealed") {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	if f.hooks.secretSet[testOAuthProject] || len(f.audit.entries) != 0 {
		t.Fatal("the store claims a secret the vault does not hold")
	}
}

func TestAuthHooks_FirstSecretMarkFailureIsAnInternalError(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.markErr = errors.New("db down")
	if w := doJSON(f.router, http.MethodPost, testHooksSecret, nil); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "whsec_") {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	if f.hooks.locked {
		t.Fatal("the secret lock was not released")
	}
}

// The vault already holds the new secret, so a rotation still answers it:
// losing it would leave every enabled hook unverifiable.
func TestAuthHooks_RotationAnswersTheNewSecretWhenOnlyTheMarkFails(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	f.vault.data[testHooksSecretAt] = map[string]string{"secret": "whsec_old"}
	f.hooks.markErr = errors.New("db down")
	w := doJSON(f.router, http.MethodPost, testHooksSecret, nil)
	secret, _ := decodeObject(t, w.Body.Bytes())["secret"].(string)
	if w.Code != http.StatusOK || secret == "" || f.vault.data[testHooksSecretAt]["secret"] != secret {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	if len(f.audit.entries) != 1 || strings.Contains(f.audit.entries[0].Details, secret) {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
}

func TestAuthHooks_RotationFailsBeforeWritingWhenTheStateCannotBeRead(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.err = errors.New("db down")
	if w := doJSON(f.router, http.MethodPost, testHooksSecret, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("POST: %d", w.Code)
	}
	if len(f.vault.data) != 0 {
		t.Fatal("the vault was written without knowing the secret state")
	}
}

// Rotating and removing hold the project's secret lock across the vault call,
// so one never interleaves with the other.
func TestAuthHooks_SecretChangesHoldTheProjectLock(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	doJSON(f.router, http.MethodPost, testHooksSecret, nil)
	doJSON(f.router, http.MethodDelete, testHooksSecret, nil)
	if got := strings.Join(f.hooks.events, ","); got != "lock,mark,unlock,lock,clear,unlock" {
		t.Fatalf("events = %s", got)
	}
	f.hooks.lockErr = errors.New("db down")
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if w := doJSON(f.router, method, testHooksSecret, nil); w.Code != http.StatusInternalServerError {
			t.Errorf("%s with no lock: %d", method, w.Code)
		}
	}
	if len(f.vault.data) != 0 {
		t.Fatal("the vault was written without the lock")
	}
}

func TestAuthHooks_EnablingNeedsTheSecret(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": testHookURL})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "signing secret") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if len(f.hooks.rows) != 0 || len(f.audit.entries) != 0 {
		t.Fatal("a hook was switched on without a secret")
	}
	// Saving the URL while it stays off is allowed.
	if w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": false, "url": testHookURL}); w.Code != http.StatusOK {
		t.Fatalf("PUT off: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthHooks_ARaceWithTheSecretRemovalIsAConflict(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	f.hooks.putErr = domain.ErrAuthHookNeedsSecret
	if w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": testHookURL}); w.Code != http.StatusConflict {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	f.hooks.putErr = errors.New("db down")
	if w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": testHookURL}); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthHooks_PutStoresAuditsTheHostOnlyAndAnswersTheList(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": "https://Hooks.Example.com/claims?token=receiver-token"})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	got := decodeHooks(t, w.Body.Bytes())
	if !got.SecretSet || len(got.Hooks) != 3 || got.Hooks[0]["enabled"] != true || got.Hooks[0]["url"] != testHookURL {
		t.Fatalf("body = %s", w.Body.String())
	}
	if row := f.hooks.rows[testOAuthProject+"/custom_access_token"]; !row.Enabled || row.URL != testHookURL {
		t.Fatalf("row = %+v", row)
	}
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
	entry := f.audit.entries[0]
	var details map[string]any
	if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
		t.Fatal(err)
	}
	if entry.Action != "auth_hook.update" || entry.ResourceID != "custom_access_token" || details["hook"] != "custom_access_token" ||
		details["enabled"] != true || details["host"] != "hooks.example.com" || len(details) != 3 || strings.Contains(entry.Details, "receiver-token") {
		t.Fatalf("audit = %+v", entry)
	}
}

func TestAuthHooks_PutRefusesBadInput(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	if w := doJSON(f.router, http.MethodPut, testHooks+"/after_sign_in", map[string]any{"enabled": false, "url": ""}); w.Code != http.StatusNotFound {
		t.Errorf("unknown hook: %d", w.Code)
	}
	if w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": ""}); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "needs its URL") {
		t.Errorf("enabled without a URL: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []map[string]any{
		{"enabled": false, "url": "http://hooks.example.com"},
		{"enabled": true, "url": "https://10.0.0.1/hook"},
		{"enabled": true, "url": "https://hooks.example.com:8443/hook"},
		{"enabled": true, "url": "https://127.1/hook"},
		{"enabled": true, "url": "https://user:pw@hooks.example.com"},
		{"enabled": "yes", "url": testHookURL},
		{"enabled": true, "url": testHookURL + strings.Repeat("a", 9000)},
	} {
		if w := doJSON(f.router, http.MethodPut, testHookClaims, body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := doJSON(f.router, http.MethodPut, testHookClaims, map[string]any{"enabled": true, "url": "https://hooks.example.com:8443/"}); !strings.Contains(w.Body.String(), "port 443") {
		t.Errorf("the port refusal is not named: %s", w.Body.String())
	}
	if len(f.hooks.rows) != 0 || len(f.audit.entries) != 0 {
		t.Fatal("a refused hook was written")
	}
}

func TestAuthHooks_RemovingTheSecretSwitchesEveryHookOffFirst(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	f.vault.data[testHooksSecretAt] = map[string]string{"secret": "whsec_old"}
	f.hooks.rows[testOAuthProject+"/send_email"] = domain.ProjectAuthHook{Hook: "send_email", Enabled: true, URL: testHookURL}
	w := doJSON(f.router, http.MethodDelete, testHooksSecret, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	got := decodeHooks(t, w.Body.Bytes())
	if got.SecretSet || got.Hooks[2]["enabled"] != false || got.Hooks[2]["url"] != testHookURL {
		t.Fatalf("body = %s", w.Body.String())
	}
	if _, kept := f.vault.data[testHooksSecretAt]; kept {
		t.Fatal("the secret outlived the delete")
	}
	if len(f.audit.entries) != 1 || f.audit.entries[0].Action != "auth_hook_secret.delete" {
		t.Fatalf("audit = %+v", f.audit.entries)
	}
}

func TestAuthHooks_SecretStaysWhenTheHooksCannotBeSwitchedOff(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.vault.data[testHooksSecretAt] = map[string]string{"secret": "whsec_old"}
	f.hooks.putErr = errors.New("db down")
	if w := doJSON(f.router, http.MethodDelete, testHooksSecret, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE: %d", w.Code)
	}
	if _, kept := f.vault.data[testHooksSecretAt]; !kept {
		t.Fatal("the secret went while a hook may still be on")
	}
}

func TestAuthHooks_VaultDeleteFailureIsAnInternalError(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.handler.authOAuth.Secrets = failingDeleteSecrets{f.vault}
	if w := doJSON(f.router, http.MethodDelete, testHooksSecret, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE: %d", w.Code)
	}
}

// serviceInfo reads /info as svc-auth does, with a projects:info:read capability token.
func serviceInfo(router chi.Router) *httptest.ResponseRecorder {
	return infoAs(router, &domain.AccessToken{Name: "svc-auth", Permissions: []string{"projects:info:read"}})
}

func infoAs(router chi.Router, token *domain.AccessToken) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, testOAuthInfo, nil)
	if token != nil {
		req = req.WithContext(auth.SetToken(req.Context(), token))
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Hook URLs may carry the receiver's token, so only the service caller gets them.
func TestAuthHooks_InfoCarriesTheHooksOnlyForTheServiceCaller(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.secretSet[testOAuthProject] = true
	f.hooks.rows[testOAuthProject+"/send_email"] = domain.ProjectAuthHook{Hook: "send_email", Enabled: true, URL: testHookURL}
	for name, token := range map[string]*domain.AccessToken{
		"viewer session":   nil,
		"session token":    {Name: "studio", Scopes: auth.ScopeSession},
		"other capability": {Name: "svc-x", Permissions: []string{"policies:read"}},
	} {
		w := infoAs(f.router, token)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "receiver-token") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		assertNoSignInKeys(t, w.Body.Bytes(), "authHooks")
	}
	if hooks, _ := decodeObject(t, serviceInfo(f.router).Body.Bytes())["authHooks"].(map[string]any); hooks["sendEmail"] != testHookURL {
		t.Fatalf("service caller: %v", hooks)
	}
}

func TestAuthHooks_InfoCarriesTheEnabledHooksOnlyWhileTheFlagIsOn(t *testing.T) {
	off := setupHooksHandler(t, features.AuthOAuth, features.AuthOTP, features.AuthGuest, features.AuthUser)
	off.hooks.secretSet[testOAuthProject] = true
	off.hooks.rows[testOAuthProject+"/send_email"] = domain.ProjectAuthHook{Hook: "send_email", Enabled: true, URL: testHookURL}
	assertNoSignInKeys(t, serviceInfo(off.router).Body.Bytes(), "authHooks")

	on := setupHooksHandler(t, features.AuthHooks)
	w := serviceInfo(on.router)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authHooks":{}`) {
		t.Fatalf("none enabled: %d %s", w.Code, w.Body.String())
	}

	on.hooks.secretSet[testOAuthProject] = true
	on.hooks.rows[testOAuthProject+"/custom_access_token"] = domain.ProjectAuthHook{Hook: "custom_access_token", Enabled: true, URL: testHookURL}
	on.hooks.rows[testOAuthProject+"/before_user_created"] = domain.ProjectAuthHook{Hook: "before_user_created", URL: "https://signup.example.com"}
	on.hooks.rows[testOAuthProject+"/send_email"] = domain.ProjectAuthHook{Hook: "send_email", Enabled: true, URL: "https://mail.example.com/send"}
	hooks, _ := decodeObject(t, serviceInfo(on.router).Body.Bytes())["authHooks"].(map[string]any)
	if len(hooks) != 2 || hooks["customAccessToken"] != testHookURL || hooks["sendEmail"] != "https://mail.example.com/send" {
		t.Fatalf("authHooks = %v", hooks)
	}

	// A hook is never reported without the secret it is signed with.
	delete(on.hooks.secretSet, testOAuthProject)
	if w := serviceInfo(on.router); !strings.Contains(w.Body.String(), `"authHooks":{}`) {
		t.Fatalf("without a secret: %s", w.Body.String())
	}
}

func TestAuthHooks_InfoIsUnavailableWhenTheHooksCannotBeRead(t *testing.T) {
	f := setupHooksHandler(t, features.AuthHooks)
	f.hooks.err = errors.New("db down")
	if w := serviceInfo(f.router); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	f.hooks.err = nil
	f.handler.authOAuth.Hooks = nil
	if w := serviceInfo(f.router); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authHooks":{}`) {
		t.Fatalf("no store: %d %s", w.Code, w.Body.String())
	}
}
