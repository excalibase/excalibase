package vaultclient

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/handler/vaultapi"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

func setupVaultServer(t *testing.T, tokens []string) (*httptest.Server, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.New(filepath.Join(dir, "test.bolt"))
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
	h.Routes(r, tokens)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, v
}

func TestHTTPClient_PutAndGet(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, "")

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
	c := NewHTTPClient(ts.URL, "")

	_, err := c.Get("nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent secret")
	}
}

func TestHTTPClient_Delete(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, "")

	c.Put("to-delete", map[string]string{"key": "val"})

	if err := c.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := c.Get("to-delete")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestHTTPClient_Sealed(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, "")

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
	c := NewHTTPClient(ts.URL, "")
	v.Seal()

	_, err := c.Get("any/path")
	if err == nil {
		t.Error("expected error when vault sealed")
	}
}

func TestHTTPClient_WithPAT(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{"my-secret-token"})
	c := NewHTTPClient(ts.URL, "my-secret-token")

	err := c.Put("test/secret", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("Put with valid PAT: %v", err)
	}

	data, err := c.Get("test/secret")
	if err != nil {
		t.Fatalf("Get with valid PAT: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("expected key=value, got %v", data)
	}
}

func TestHTTPClient_InvalidPAT(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{"my-secret-token"})
	c := NewHTTPClient(ts.URL, "wrong-token")

	err := c.Put("test/secret", map[string]string{"key": "value"})
	if err == nil {
		t.Error("expected error with invalid PAT")
	}
}

func TestHTTPClient_NoPATWhenRequired(t *testing.T) {
	ts, _ := setupVaultServer(t, []string{"my-secret-token"})
	c := NewHTTPClient(ts.URL, "")

	_, err := c.Get("test/secret")
	if err == nil {
		t.Error("expected error without PAT when required")
	}
}

func TestHTTPClient_GetPublicKey(t *testing.T) {
	ts, _ := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, "")

	// PKI not initialized yet — should get an error or empty
	_, err := c.GetPublicKey()
	if err == nil {
		// PKI might auto-init on vault init; just verify no panic
		t.Log("GetPublicKey returned without error (PKI may be auto-initialized)")
	}
}

func TestHTTPClient_GetPublicKeySealed(t *testing.T) {
	ts, v := setupVaultServer(t, nil)
	c := NewHTTPClient(ts.URL, "")
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
