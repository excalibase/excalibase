package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	testVaultInitPath    = "/api/vault/init"
	testVaultUnsealPath  = "/api/vault/unseal"
	testVaultCredsPath   = "/api/vault/secrets/projects/my-app/credentials/admin"
	testVaultPKIPath     = "/api/vault/pki/public-key"
	testVaultTestKeyPath = "/api/vault/secrets/test/key"
	testExpect503Fmt     = "expected 503, got %d"
	testVaultRekeyPath   = "/api/vault/rekey"
	testExpect400Fmt     = "expected 400, got %d"
	testVaultListPath    = "/api/vault/secrets-list"
)

// fakeAuthMiddleware injects a fake admin user so auth-protected vault routes pass.
func fakeAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := &domain.User{ID: "test-admin", Username: "admin", Role: "platform_admin", Active: true}
		ctx := auth.SetUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func setupVaultRouter(t *testing.T) (chi.Router, *vault.Vault) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	h := NewVaultHandler(v)
	r := chi.NewRouter()
	r.Use(fakeAuthMiddleware)
	r.Route("/api/vault", h.Routes)
	return r, v
}

func TestVaultStatus_NotInitialized(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("GET", "/api/vault/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var body map[string]interface{}
	json.NewDecoder(w.Body).Decode(&body)
	if body["initialized"] != false {
		t.Error("expected initialized=false")
	}
	if body["sealed"] != true {
		t.Error("expected sealed=true")
	}
}

func TestVaultInit(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("POST", testVaultInitPath,
		strings.NewReader(`{"shares":5,"threshold":3}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body: %s", w.Code, w.Body.String())
	}

	var body struct {
		Shares    []string `json:"shares"`
		Threshold int      `json:"threshold"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if len(body.Shares) != 5 {
		t.Fatalf("expected 5 shares, got %d", len(body.Shares))
	}
	if body.Threshold != 3 {
		t.Fatalf("expected threshold 3, got %d", body.Threshold)
	}

	// Status should now show initialized + unsealed
	req2 := httptest.NewRequest("GET", "/api/vault/status", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	var status map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&status)
	if status["initialized"] != true {
		t.Error("expected initialized=true")
	}
	if status["sealed"] != false {
		t.Error("expected sealed=false after init")
	}
}

func TestVaultSealAndUnseal(t *testing.T) {
	r, v := setupVaultRouter(t)

	// Init
	result, _ := v.Init(3, 2)

	// Seal
	req := httptest.NewRequest("POST", "/api/vault/seal", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seal: got %d", w.Code)
	}
	if !v.Sealed() {
		t.Fatal("should be sealed")
	}

	// Unseal with 2 shares
	for i, share := range result.Shares[:2] {
		body := `{"share":"` + share + `"}`
		req := httptest.NewRequest("POST", testVaultUnsealPath, strings.NewReader(body))
		req.Header.Set(sharedContentType, sharedMIMEJSON)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("unseal %d: got %d, body: %s", i, w.Code, w.Body.String())
		}
	}

	if v.Sealed() {
		t.Fatal("should be unsealed after 2 shares")
	}
}

func TestVaultSecretCRUD(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	// PUT secret
	putBody := fmt.Sprintf(`{"host":"10.0.0.5","port":"5432","username":"admin","password":%q}`, testutil.FixtureSecret("vault-cred"))
	req := httptest.NewRequest("PUT", testVaultCredsPath,
		strings.NewReader(putBody))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: got %d, body: %s", w.Code, w.Body.String())
	}

	// GET secret
	req = httptest.NewRequest("GET", testVaultCredsPath, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: got %d", w.Code)
	}
	var data map[string]string
	json.NewDecoder(w.Body).Decode(&data)
	if data["host"] != "10.0.0.5" {
		t.Errorf("host: got %s", data["host"])
	}
	if data["password"] != testutil.FixtureSecret("vault-cred") {
		t.Errorf("password: got %s", data["password"])
	}

	// DELETE secret
	req = httptest.NewRequest("DELETE", testVaultCredsPath, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: got %d", w.Code)
	}

	// GET should 404
	req = httptest.NewRequest("GET", testVaultCredsPath, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete: got %d, want 404", w.Code)
	}
}

func TestVaultPublicKey(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("GET", testVaultPKIPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["algorithm"] != "EC-P256" {
		t.Errorf("algorithm: got %s", body["algorithm"])
	}
	if body["key"] == "" {
		t.Fatal("key should not be empty")
	}
}

func TestVaultSecrets_WhenSealed_Returns503(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("GET", testVaultTestKeyPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf(testExpect503Fmt, w.Code)
	}
}

// --- Rekey ---

func TestVaultRekey_Success(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(3, 2)

	req := httptest.NewRequest("POST", testVaultRekeyPath,
		strings.NewReader(`{"shares":5,"threshold":3}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("rekey: got %d, body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Shares    []string `json:"shares"`
		Threshold int      `json:"threshold"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if len(body.Shares) != 5 {
		t.Errorf("expected 5 shares, got %d", len(body.Shares))
	}
	if body.Threshold != 3 {
		t.Errorf("expected threshold 3, got %d", body.Threshold)
	}
}

func TestVaultRekey_NotInitialized_Returns400(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("POST", testVaultRekeyPath,
		strings.NewReader(`{"shares":3,"threshold":2}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for uninitialized vault, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestVaultRekey_InvalidJSON_Returns400(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("POST", testVaultRekeyPath,
		strings.NewReader(testNotJSON))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

// --- Init error paths ---

func TestVaultInit_AlreadyInitialized_Returns400(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("POST", testVaultInitPath,
		strings.NewReader(`{"shares":3,"threshold":2}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for double-init, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestVaultInit_InvalidJSON_Returns400(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("POST", testVaultInitPath,
		strings.NewReader("{invalid}"))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf(testExpect400Fmt, w.Code)
	}
}

func TestVaultInit_DefaultsSharesAndThreshold(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("POST", testVaultInitPath,
		strings.NewReader(`{"shares":0,"threshold":0}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Shares    []string `json:"shares"`
		Threshold int      `json:"threshold"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	// defaults: shares=5, threshold=3
	if len(body.Shares) != 5 {
		t.Errorf("expected 5 default shares, got %d", len(body.Shares))
	}
}

// --- Unseal error paths ---

func TestVaultUnseal_InvalidShare_Returns400(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(3, 2)
	v.Seal()

	req := httptest.NewRequest("POST", testVaultUnsealPath,
		strings.NewReader(`{"share":"notvalidbase64!!!"}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid share, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestVaultUnseal_InvalidJSON_Returns400(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("POST", testVaultUnsealPath,
		strings.NewReader(testNotJSON))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf(testExpect400Fmt, w.Code)
	}
}

// --- GetPublicKey error paths ---

func TestVaultGetPublicKey_WhenSealed_Returns503(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("GET", testVaultPKIPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf(testExpect503Fmt, w.Code)
	}
}

func TestVaultGetPublicKey_NotInitialized_Returns404(t *testing.T) {
	r, _ := setupVaultRouter(t)

	req := httptest.NewRequest("GET", testVaultPKIPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Not initialized means vault has no PKI key yet; expects 404 or 503
	if w.Code != http.StatusNotFound && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 404 or 503, got %d, body: %s", w.Code, w.Body.String())
	}
}

// --- PutSecret error paths ---

func TestVaultPutSecret_WhenSealed_Returns503(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("PUT", "/api/vault/secrets/projects/app/creds",
		strings.NewReader(`{"key":"value"}`))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf(testExpect503Fmt, w.Code)
	}
}

func TestVaultPutSecret_InvalidJSON_Returns400(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("PUT", testVaultTestKeyPath,
		strings.NewReader(testNotJSON))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf(testExpect400Fmt, w.Code)
	}
}

// --- DeleteSecret error paths ---

func TestVaultDeleteSecret_WhenSealed_Returns503(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("DELETE", testVaultTestKeyPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf(testExpect503Fmt, w.Code)
	}
}

// --- ListSecrets ---
//
// Covers the /secrets-list listing path: empty + populated + prefix
// filter + sealed-vault. Vault returns nil when no entries exist —
// the handler normalises that to an empty slice so the studio doesn't
// have to special-case `paths == null`.

func TestVaultListSecrets_Empty(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("GET", testVaultListPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListSecrets empty: got %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.Paths == nil {
		t.Error("paths should be [] not nil — frontend depends on this normalisation")
	}
}

func TestVaultListSecrets_Populated(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Put("projects/a/x/credentials/admin", map[string]string{"k": "v"})
	v.Put("projects/b/y/credentials/admin", map[string]string{"k": "v"})

	req := httptest.NewRequest("GET", testVaultListPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if len(body.Paths) < 2 {
		t.Errorf("expected at least 2 paths, got %v", body.Paths)
	}
}

func TestVaultListSecrets_PrefixFilter(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Put("projects/foo/x/credentials/admin", map[string]string{"k": "v"})
	v.Put("projects/bar/y/credentials/admin", map[string]string{"k": "v"})

	req := httptest.NewRequest("GET", "/api/vault/secrets-list?prefix=projects/foo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	for _, p := range body.Paths {
		if !strings.HasPrefix(p, "projects/foo") {
			t.Errorf("prefix-filtered list leaked %q", p)
		}
	}
}

func TestVaultListSecrets_WhenSealed_Returns503(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)
	v.Seal()

	req := httptest.NewRequest("GET", testVaultListPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 sealed, got %d", w.Code)
	}
}
