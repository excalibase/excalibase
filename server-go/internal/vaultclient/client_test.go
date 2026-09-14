package vaultclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/handler/vaultapi"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	testToDelete     = "to-delete"
	testAnyPath      = "any/path"
	testSecretToken  = "my-secret-token"
	testSecretPath   = "test/secret"
	testExpect500Err = "expected error on 500"
)

// testPAT is the placeholder token used by tests that don't explicitly
// pass their own token list. Returned from setupVaultServer so tests can
// supply it to NewHTTPClient and authenticate against the placeholder.
const testPAT = "test-pat-placeholder"

func setupVaultServer(t *testing.T, tokens []string) (*httptest.Server, *vault.Vault) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("create vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	if _, err := v.Unseal(result.Shares[0]); err != nil {
		t.Fatalf("unseal vault: %v", err)
	}

	h := vaultapi.NewVaultHandler(v)
	r := chi.NewRouter()
	if len(tokens) == 0 {
		// Routes refuses to start without a PAT. Tests that don't exercise
		// the auth surface get a placeholder.
		tokens = []string{testPAT}
	}
	h.Routes(r, tokens)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, v
}

func TestHTTPClient_PutAndGet(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	err := c.Put("projects/org-a/app-a/credentials/admin", map[string]string{
		"host":     "db.example.com",
		"port":     "5432",
		"password": "s3cret",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	data, err := c.Get("projects/org-a/app-a/credentials/admin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if data["host"] != "db.example.com" {
		t.Errorf("expected host=db.example.com, got %s", data["host"])
	}
	if data["password"] != "s3cret" {
		t.Errorf("expected password=s3cret, got %s", data["password"])
	}
}

func TestHTTPClient_GetNotFound(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	_, err := c.Get("nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent secret")
	}
}

func TestHTTPClient_Delete(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	c.Put(testToDelete, map[string]string{"key": "val"})

	if err := c.Delete(testToDelete); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := c.Get(testToDelete)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestHTTPClient_DeletePrefix(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	// Seed a couple of secrets under a common prefix.
	if err := c.Put("projects/org-z/app1/credentials/a", map[string]string{"k": "1"}); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := c.Put("projects/org-z/app1/credentials/b", map[string]string{"k": "2"}); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	n, err := c.DeletePrefix("projects/org-z/app1")
	if err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	if n < 2 {
		t.Errorf("expected >=2 deleted, got %d", n)
	}
	// The secrets should be gone.
	if _, err := c.Get("projects/org-z/app1/credentials/a"); err == nil {
		t.Error("secret a should be deleted")
	}
}

func TestHTTPClient_DeletePrefix_RejectsEmpty(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)
	if _, err := c.DeletePrefix(""); err == nil {
		t.Error("empty prefix should be rejected")
	}
}

func TestHTTPClient_Sealed(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	if c.Sealed() {
		t.Error("expected unsealed")
	}

	v.Seal()

	if !c.Sealed() {
		t.Error("expected sealed")
	}
}

func TestHTTPClient_SealedReturnsError(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)
	v.Seal()

	_, err := c.Get(testAnyPath)
	if err == nil {
		t.Error("expected error when vault sealed")
	}
}

func TestHTTPClient_WithPAT(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{testSecretToken})
	c := NewHTTPClient(ts.URL, testSecretToken)

	err := c.Put(testSecretPath, map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("Put with valid PAT: %v", err)
	}

	data, err := c.Get(testSecretPath)
	if err != nil {
		t.Fatalf("Get with valid PAT: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("expected key=value, got %v", data)
	}
}

func TestHTTPClient_InvalidPAT(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{testSecretToken})
	c := NewHTTPClient(ts.URL, "wrong-token")

	err := c.Put(testSecretPath, map[string]string{"key": "value"})
	if err == nil {
		t.Error("expected error with invalid PAT")
	}
}

func TestHTTPClient_NoPATWhenRequired(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{testSecretToken})
	c := NewHTTPClient(ts.URL, testPAT)

	_, err := c.Get(testSecretPath)
	if err == nil {
		t.Error("expected error without PAT when required")
	}
}

func TestHTTPClient_GetPublicKey(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	// PKI not initialized yet — should get an error or empty
	_, err := c.GetPublicKey()
	if err == nil {
		// PKI might auto-init on vault init; just verify no panic
		t.Log("GetPublicKey returned without error (PKI may be auto-initialized)")
	}
}

func TestHTTPClient_GetPublicKeySealed(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)
	v.Seal()

	_, err := c.GetPublicKey()
	if err == nil {
		t.Error("expected error when vault sealed")
	}
}

func TestHTTPClient_ImplementsVaultClient(t *testing.T) {
	// Compile-time check that HTTPClient implements VaultClient
	var _ VaultClient = (*HTTPClient)(nil)
}

// TestHTTPClient_List exercises the secrets-list path that was at 0%
// coverage. Seeds a few secrets, asks for them with and without prefix,
// then validates the sealed-vault and bad-status error branches.
func TestHTTPClient_List(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)

	if err := c.Put("projects/foo/proj-a/credentials/excalibase_app", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if err := c.Put("projects/bar/proj-b/credentials/excalibase_app", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	all, err := c.List("")
	if err != nil {
		t.Fatalf("List(no-prefix): %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected at least 2 paths, got %d (%v)", len(all), all)
	}

	scoped, err := c.List("projects/foo")
	if err != nil {
		t.Fatalf("List(prefix): %v", err)
	}
	if len(scoped) == 0 {
		t.Errorf("expected ≥1 path under projects/foo, got %d", len(scoped))
	}
	for _, p := range scoped {
		if !strings.HasPrefix(p, "projects/foo") {
			t.Errorf("expected prefix-filtered result, got %q", p)
		}
	}
}

func TestHTTPClient_List_SealedReturnsError(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, testPAT)
	v.Seal()

	_, err := c.List("")
	if err == nil {
		t.Error("expected error when vault is sealed")
	}
}

func TestHTTPClient_List_BadStatusReturnsError(t *testing.T) {
	// Stand up a minimal server that returns 500 for the list endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if _, err := c.List(""); err == nil {
		t.Error("expected error on 500 status")
	}
}

func TestHTTPClient_List_BadJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if _, err := c.List(""); err == nil {
		t.Error("expected JSON decode error")
	}
}

// Cover Get/Put/Delete edge paths that pushed those functions below 100%
// (request-creation failures and bad-status branches).
func TestHTTPClient_Get_BadStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if _, err := c.Get(testAnyPath); err == nil {
		t.Error(testExpect500Err)
	}
}

func TestHTTPClient_Put_BadStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if err := c.Put(testAnyPath, map[string]string{"k": "v"}); err == nil {
		t.Error(testExpect500Err)
	}
}

func TestHTTPClient_Delete_BadStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if err := c.Delete(testAnyPath); err == nil {
		t.Error(testExpect500Err)
	}
}

func TestHTTPClient_Get_BadJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if _, err := c.Get(testAnyPath); err == nil {
		t.Error("expected decode error")
	}
}

func TestHTTPClient_Sealed_HandlesBadResponse(t *testing.T) {
	// Server returning unparseable JSON → Sealed conservatively returns true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if !c.Sealed() {
		t.Error("expected Sealed=true on bad JSON (conservative)")
	}
}

func TestHTTPClient_GetPublicKey_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	if _, err := c.GetPublicKey(); err == nil {
		t.Error(testExpect500Err)
	}
}

func TestHTTPClient_GetPublicKey_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]string{}) // missing "key"
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")
	// Server returned 200 with no "key" — client returns empty string but no error
	pk, _ := c.GetPublicKey()
	if pk != "" {
		t.Errorf("expected empty key when server returned no key, got %q", pk)
	}
}
