package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/vault"
	"github.com/go-chi/chi/v5"
)

func setupVaultRouter(t *testing.T) (chi.Router, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.New(filepath.Join(dir, "vault.bolt"))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	h := NewVaultHandler(v)
	r := chi.NewRouter()
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

	req := httptest.NewRequest("POST", "/api/vault/init",
		strings.NewReader(`{"shares":5,"threshold":3}`))
	req.Header.Set("Content-Type", "application/json")
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
		req := httptest.NewRequest("POST", "/api/vault/unseal", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
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
	putBody := `{"host":"10.0.0.5","port":"5432","username":"admin","password":"secret"}`
	req := httptest.NewRequest("PUT", "/api/vault/secrets/projects/my-app/credentials/admin",
		strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: got %d, body: %s", w.Code, w.Body.String())
	}

	// GET secret
	req = httptest.NewRequest("GET", "/api/vault/secrets/projects/my-app/credentials/admin", nil)
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
	if data["password"] != "secret" {
		t.Errorf("password: got %s", data["password"])
	}

	// DELETE secret
	req = httptest.NewRequest("DELETE", "/api/vault/secrets/projects/my-app/credentials/admin", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: got %d", w.Code)
	}

	// GET should 404
	req = httptest.NewRequest("GET", "/api/vault/secrets/projects/my-app/credentials/admin", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete: got %d, want 404", w.Code)
	}
}

func TestVaultPublicKey(t *testing.T) {
	r, v := setupVaultRouter(t)
	v.Init(1, 1)

	req := httptest.NewRequest("GET", "/api/vault/pki/public-key", nil)
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

	req := httptest.NewRequest("GET", "/api/vault/secrets/test/key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
