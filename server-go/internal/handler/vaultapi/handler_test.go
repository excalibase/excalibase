package vaultapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
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

func setupRouter(v *vault.Vault, tokens []string) *chi.Mux {
	h := NewVaultHandler(v)
	r := chi.NewRouter()
	h.Routes(r, tokens)
	return r
}

// --- Status ---

func TestStatus_ReturnsInitializedAndSealed(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
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
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- Unseal ---

func TestUnseal_ProgressAndCompletion(t *testing.T) {
	v := setupTestVault(t)
	result, _ := v.Init(1, 1)
	r := setupRouter(v, nil)

	body, _ := json.Marshal(map[string]string{"share": result.Shares[0]})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/unseal", bytes.NewBuffer(body)))

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
	r := setupRouter(v, []string{"valid-token"})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets/test/path", nil))

	if w.Code != 401 {
		t.Errorf("expected 401 without PAT, got %d", w.Code)
	}
}

func TestSecrets_InvalidPAT_Returns401(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, []string{"valid-token"})

	req := httptest.NewRequest("GET", "/secrets/test/path", nil)
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
	r := setupRouter(v, []string{"valid-token"})

	// Put a secret
	body := `{"key":"value"}`
	req := httptest.NewRequest("PUT", "/secrets/test/path", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read it back
	req = httptest.NewRequest("GET", "/secrets/test/path", nil)
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

	body := `{"host":"db.example.com","port":"5432","password":"s3cret"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/secrets/projects/org-a/app-a/credentials/admin", bytes.NewBufferString(body)))
	if w.Code != 200 {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets/projects/org-a/app-a/credentials/admin", nil))
	if w.Code != 200 {
		t.Fatalf("GET expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["host"] != "db.example.com" || resp["password"] != "s3cret" {
		t.Errorf("unexpected response: %v", resp)
	}
}

func TestGetSecret_NotFound_Returns404(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets/nonexistent", nil))
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
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets/test", nil))
	if w.Code != 503 {
		t.Errorf("expected 503 when sealed, got %d", w.Code)
	}
}

func TestDeleteSecret(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body := `{"key":"val"}`
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/secrets/to-delete", bytes.NewBufferString(body)))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/secrets/to-delete", nil))
	if w.Code != 200 {
		t.Fatalf("DELETE expected 200, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets/to-delete", nil))
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
		t.Errorf("expected 503 when sealed, got %d", w.Code)
	}
}

// --- Healthz ---

func TestHealthz(t *testing.T) {
	v := setupTestVault(t)
	r := setupRouter(v, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- No PAT configured = open access ---

func TestSecrets_NoPATConfigured_OpenAccess(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	body := `{"open":"access"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/secrets/open", bytes.NewBufferString(body)))
	if w.Code != 200 {
		t.Errorf("expected 200 with no PAT configured, got %d", w.Code)
	}
}

// --- List Secrets ---

func TestListSecrets_ReturnsAllPaths(t *testing.T) {
	v := setupTestVault(t)
	initAndUnsealVault(t, v)
	r := setupRouter(v, nil)

	// Store some secrets
	for _, path := range []string{"projects/org-a/app-a/creds", "projects/org-b/app-b/creds", "backup/s3"} {
		body := `{"key":"val"}`
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/secrets/"+path, bytes.NewBufferString(body)))
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets-list", nil))
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/secrets/"+path, bytes.NewBufferString(body)))
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets-list?prefix=projects/org-a/", nil))
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
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
	r.ServeHTTP(w, httptest.NewRequest("GET", "/secrets-list", nil))
	if w.Code != 503 {
		t.Errorf("expected 503 when sealed, got %d", w.Code)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
