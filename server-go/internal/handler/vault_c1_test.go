package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// TestVault_InitUnsealRekeyRequireAuth pins SEC-C1: vault lifecycle operations
// that create or consume key material (init returns the unseal shares; unseal
// and rekey mutate the seal) must never be reachable unauthenticated. Only
// /status and the PKI public key stay open (the pre-login setup UI polls them,
// and the PKI key is public by design).
func TestVault_InitUnsealRekeyRequireAuth(t *testing.T) {
	dir := t.TempDir()
	v, err := vault.New(filepath.Join(dir, "vault.bolt"))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	h := NewVaultHandler(v)
	r := chi.NewRouter()
	// No auth middleware mounted → requests arrive unauthenticated.
	r.Route("/api/vault", h.Routes)

	sensitive := []struct {
		method, path, body string
	}{
		{"POST", "/api/vault/init", `{"shares":5,"threshold":3}`},
		{"POST", "/api/vault/unseal", `{"share":"deadbeef"}`},
		{"POST", "/api/vault/rekey", `{"shares":5,"threshold":3}`},
	}
	for _, tc := range sensitive {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s: got %d, want 401 (SEC-C1)", tc.method, tc.path, w.Code)
		}
	}

	// Read-only status must remain public for the first-run setup UI.
	req := httptest.NewRequest("GET", "/api/vault/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("/status must stay public for the pre-login setup UI; got 401")
	}
}
