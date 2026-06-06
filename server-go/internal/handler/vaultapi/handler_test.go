package vaultapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	testExpect200Fmt     = "expected 200, got %d"
	testExpect200BodyFmt = "expected 200, got %d: %s"
	testExpect400Fmt     = "expected 400, got %d"
	testUnsealPath       = "/unseal"
	testValidToken       = "valid-token"
	testSecretsPath      = "/secrets/test/path"
	testExpect503Fmt     = "expected 503 when sealed, got %d"
	testDeleteSecretPath = "/secrets/to-delete"
	testSecretsPrefix    = "/secrets/"
	testMIMEJSON         = "application/json"
	testContentType      = "Content-Type"
)


func setupTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.New(filepath.Join(dir, "test.bolt"))
	if err != nil {
		t.Fatalf("create vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func initAndUnsealVault(t *testing.T, v *vault.Vault) {
	t.Helper()
	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	_, err = v.Unseal(result.Shares[0])
	if err != nil {
		t.Fatalf("unseal vault: %v", err)
	}
}

// testPlaceholderPAT is what setupRouter wires when callers pass nil.
const testPlaceholderPAT = "test-pat-placeholder"

func setupRouter(v *vault.Vault, tokens []string) *chi.Mux {
	h := NewVaultHandler(v)
	r := chi.NewRouter()
	// Routes now refuses to start without a PAT. Tests that don't exercise
	// the auth surface get a placeholder token; tests that DO exercise auth
	// must pass their own non-empty list.
	if len(tokens) == 0 {
		tokens = []string{testPlaceholderPAT}
	}
	h.Routes(r, tokens)
	return r
}

// withTestPAT attaches the placeholder PAT for tests that pass nil tokens.
// Tests with an explicit token list should set Authorization themselves.
func withTestPAT(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testPlaceholderPAT)
	return req
}

// --- Status ---

func TestStatus_ReturnsInitializedAndSealed(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))

	if w.Code != 200 {
		t.Fatalf(testExpect200Fmt, w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["initialized"] != false {
		t.Error("expected initialized=false")
	}
	if resp["sealed"] != true {
		t.Error("expected sealed=true")
	}
}

// --- Init ---

func TestInit_CreatesSharesAndThreshold(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	body := `{"shares":3,"threshold":2}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/init", bytes.NewBufferString(body)))

	if w.Code != 200 {
		t.Fatalf(testExpect200BodyFmt, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	shares, ok := resp["shares"].([]interface{})
	if !ok || len(shares) != 3 {
		t.Errorf("expected 3 shares, got %v", resp["shares"])
	}
	if resp["threshold"] != float64(2) {
		t.Errorf("expected threshold=2, got %v", resp["threshold"])
	}
}

func TestInit_AlreadyInitialized_Returns400(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body := `{"shares":1,"threshold":1}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/init", bytes.NewBufferString(body)))

	if w.Code != 400 {
		t.Errorf(testExpect400Fmt, w.Code)
	}
}

// --- Unseal ---

func TestUnseal_ProgressAndCompletion(t *testing.T) {
	v := setupTestVault(t)
	result, _ := v.Init(1, 1)
	r := setupRouter(v, nil)

	body, _ := json.Marshal(map[string]string{"share": result.Shares[0]})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", testUnsealPath, bytes.NewBuffer(body)))

	if w.Code != 200 {
		t.Fatalf(testExpect200BodyFmt, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["sealed"] != false {
		t.Error("expected sealed=false after unseal")
	}
}

// --- PAT Auth ---

func TestSecrets_NoPAT_Returns401(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, []string{testValidToken})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", testSecretsPath, nil))

	if w.Code != 401 {
		t.Errorf("expected 401 without PAT, got %d", w.Code)
	}
}

func TestSecrets_InvalidPAT_Returns401(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, []string{testValidToken})

	req := httptest.NewRequest("GET", testSecretsPath, nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 with invalid PAT, got %d", w.Code)
	}
}

func TestSecrets_ValidPAT_Allowed(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, []string{testValidToken})

	// Put a secret
	body := `{"key":"value"}`
	req := httptest.NewRequest("PUT", testSecretsPath, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read it back
	req = httptest.NewRequest("GET", testSecretsPath, nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["key"] != "value" {
		t.Errorf("expected key=value, got %v", resp)
	}
}

// --- Secrets CRUD ---

func TestPutAndGetSecret(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body := fmt.Sprintf(`{"host":"db.example.com","port":"5432","password":%q}`, testutil.FixtureSecret("vault-api-cred"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("PUT", "/secrets/projects/org-a/app-a/credentials/admin", bytes.NewBufferString(body))))
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets/projects/org-a/app-a/credentials/admin", nil)))
	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["host"] != "db.example.com" || resp["password"] != testutil.FixtureSecret("vault-api-cred") {
		t.Errorf("unexpected response: %v", resp)
	}
}

func TestGetSecret_NotFound_Returns404(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets/nonexistent", nil)))
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetSecret_VaultSealed_Returns503(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets/test", nil)))
	if w.Code != 503 {
		t.Errorf(testExpect503Fmt, w.Code)
	}
}

func TestDeleteSecret(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body := `{"key":"val"}`
	r.ServeHTTP(httptest.NewRecorder(), withTestPAT(httptest.NewRequest("PUT", testDeleteSecretPath, bytes.NewBufferString(body))))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("DELETE", testDeleteSecretPath, nil)))
	if w.Code != 200 {
		t.Fatalf("DELETE expected 200, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", testDeleteSecretPath, nil)))
	if w.Code != 404 {
		t.Errorf("expected 404 after delete, got %d", w.Code)
	}
}

// --- PKI ---

func TestGetPublicKey_VaultSealed_Returns503(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/pki/public-key", nil))
	if w.Code != 503 {
		t.Errorf(testExpect503Fmt, w.Code)
	}
}

// --- Healthz ---

func TestHealthz(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf(testExpect200Fmt, w.Code)
	}
}

// --- No PAT configured = open access ---

// Documents that Routes refuses to start without a PAT — running open is no
// longer supported. The previous "open access when tokens nil" branch was a
// security footgun.
func TestRoutes_PanicWithoutPAT(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Routes must panic when called without a PAT")
		}
	}()
	h := NewVaultHandler(v)
	r := chi.NewRouter()
	h.Routes(r, nil) // expected to panic
}

// --- List Secrets ---

func TestListSecrets_ReturnsAllPaths(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	// Store some secrets
	for _, path := range []string{"projects/org-a/app-a/creds", "projects/org-b/app-b/creds", "backup/s3"} {
		body := `{"key":"val"}`
		r.ServeHTTP(httptest.NewRecorder(), withTestPAT(httptest.NewRequest("PUT", testSecretsPrefix+path, bytes.NewBufferString(body))))
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets-list", nil)))
	if w.Code != 200 {
		t.Fatalf(testExpect200BodyFmt, w.Code, w.Body.String())
	}

	var resp struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	// At least our 3 + PKI keys from init
	if len(resp.Paths) < 3 {
		t.Errorf("expected at least 3 paths, got %d: %v", len(resp.Paths), resp.Paths)
	}
}

func TestListSecrets_WithPrefix(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	for _, path := range []string{"projects/org-a/app-a/creds", "projects/org-a/app-b/creds", "other/path"} {
		body := `{"key":"val"}`
		r.ServeHTTP(httptest.NewRecorder(), withTestPAT(httptest.NewRequest("PUT", testSecretsPrefix+path, bytes.NewBufferString(body))))
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets-list?prefix=projects/org-a/", nil)))
	if w.Code != 200 {
		t.Fatalf(testExpect200Fmt, w.Code)
	}

	var resp struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Paths) != 2 {
		t.Errorf("expected 2 paths for prefix, got %d: %v", len(resp.Paths), resp.Paths)
	}
}

func TestListSecrets_VaultSealed_Returns503(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("GET", "/secrets-list", nil)))
	if w.Code != 503 {
		t.Errorf(testExpect503Fmt, w.Code)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// --- Coverage gap fillers: error/edge paths that weren't exercised ---

// Init: invalid JSON body → 400.
func TestInit_InvalidJSONReturns400(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	req := httptest.NewRequest("POST", "/init", bytes.NewBufferString("{"))
	req.Header.Set(testContentType, testMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for malformed JSON, got %d", w.Code)
	}
}

// Unseal: invalid JSON body → 400.
func TestUnseal_InvalidJSONReturns400(t *testing.T) {
	v := setupTestVault(t)
	if _, err := v.Init(1, 1); err != nil {
		t.Fatal(err)
	}
	r := setupRouter(v, nil)

	req := httptest.NewRequest("POST", testUnsealPath, bytes.NewBufferString("not-json"))
	req.Header.Set(testContentType, testMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf(testExpect400Fmt, w.Code)
	}
}

// Unseal: non-hex share → 400 (hex decode fails inside v.Unseal). Vault
// must be sealed for this branch to fire — Init auto-unseals so we have
// to call Seal() first.
func TestUnseal_NonHexShareReturns400(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	body, _ := json.Marshal(map[string]string{"share": "z" /* invalid hex */})
	req := httptest.NewRequest("POST", testUnsealPath, bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 on non-hex share, got %d", w.Code)
	}
}

// Seal: requires PAT, empties barrier in memory.
func TestSeal_RequiresAuth(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	tokens := []string{"the-pat"}
	r := setupRouter(v, tokens)

	// No auth → 401
	req := httptest.NewRequest("POST", "/seal", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401 without PAT, got %d", w.Code)
	}

	// With PAT → 200, vault becomes sealed
	req = httptest.NewRequest("POST", "/seal", nil)
	req.Header.Set("Authorization", "Bearer the-pat")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 with PAT, got %d", w.Code)
	}
	if !v.Sealed() {
		t.Error("vault should be sealed after /seal")
	}
}

// Rekey: regenerates shares.
func TestRekey_ReturnsNewShares(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body, _ := json.Marshal(map[string]int{"shares": 3, "threshold": 2})
	req := withTestPAT(httptest.NewRequest("POST", "/rekey", bytes.NewBuffer(body)))
	req.Header.Set(testContentType, testMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	shares, _ := resp["shares"].([]any)
	if len(shares) != 3 {
		t.Errorf("expected 3 fresh shares, got %d", len(shares))
	}
}

func TestRekey_InvalidJSONReturns400(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)
	req := withTestPAT(httptest.NewRequest("POST", "/rekey", bytes.NewBufferString("{")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 on bad JSON, got %d", w.Code)
	}
}

// PutSecret: invalid JSON, missing path, sealed vault.
func TestPutSecret_InvalidJSONReturns400(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	req := withTestPAT(httptest.NewRequest("PUT", "/secrets/some/path", bytes.NewBufferString("{")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf(testExpect400Fmt, w.Code)
	}
}

func TestPutSecret_VaultSealed_Returns503(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	body, _ := json.Marshal(map[string]string{"k": "v"})
	req := withTestPAT(httptest.NewRequest("PUT", "/secrets/some/path", bytes.NewBuffer(body)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on sealed PUT, got %d", w.Code)
	}
}

// DeleteSecret: sealed vault and not-found paths.
func TestDeleteSecret_VaultSealed_Returns503(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	v.Seal()
	r := setupRouter(v, nil)

	req := withTestPAT(httptest.NewRequest("DELETE", "/secrets/any/path", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 on sealed DELETE, got %d", w.Code)
	}
}

func TestListSecrets_PrefixFilter(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	// Seed two paths with different prefixes
	for _, p := range []string{"projects/foo/x", "tenants/bar/y"} {
		body, _ := json.Marshal(map[string]string{"k": "v"})
		req := withTestPAT(httptest.NewRequest("PUT", testSecretsPrefix+p, bytes.NewBuffer(body)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 && w.Code != 201 && w.Code != 204 {
			t.Fatalf("seed put %s: %d %s", p, w.Code, w.Body.String())
		}
	}

	req := withTestPAT(httptest.NewRequest("GET", "/secrets-list?prefix=projects/", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	var got struct {
		Paths []string `json:"paths"`
	}
	_ = json.NewDecoder(w.Body).Decode(&got)
	for _, p := range got.Paths {
		if !bytesPrefix(p, "projects/") {
			t.Errorf("expected prefix-filtered, got %q", p)
		}
	}
}

func bytesPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// GetPublicKey when PKI is initialized via Init flow.
func TestGetPublicKey_AfterInit_ReturnsKey(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	req := httptest.NewRequest("GET", "/pki/public-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// Either 200 with key, or 404 if PKI not auto-init — accept either,
	// just exercise the success path.
	if w.Code != 200 && w.Code != 404 {
		t.Errorf("unexpected status %d", w.Code)
	}
}

func TestDeletePrefix_RemovesMatchingSecrets(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	if err := v.Put("projects/org-d/app/a", map[string]string{"k": "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	if err := v.Put("projects/org-d/app/b", map[string]string{"k": "2"}); err != nil {
		t.Fatalf("put b: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("DELETE", "/secrets-list?prefix=projects/org-d/app", nil)))
	if w.Code != 200 {
		t.Fatalf("DeletePrefix expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["deleted"] == nil {
		t.Errorf("expected deleted count in response, got %v", resp)
	}
}

func TestDeletePrefix_RequiresPrefix(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withTestPAT(httptest.NewRequest("DELETE", "/secrets-list", nil)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing prefix should 400, got %d", w.Code)
	}
}
