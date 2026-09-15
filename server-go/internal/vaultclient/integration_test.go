package vaultclient

import (
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/handler/vaultapi"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	testVaultHost       = "app-a-postgres-rw.ns-app-a.svc.cluster.local"
	testVaultCredsPath  = "projects/org-a/app-a/credentials/admin"
	testIntegSecretPath = "test/secret"
)

// Integration tests: vault service (handler) ↔ HTTP client ↔ platform operations
// Simulates the full flow: platform stores credentials in vault, auth/graphql reads them.

func setupIntegrationServer(t *testing.T, tokens []string) (*httptest.Server, *vault.Vault) {
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
		tokens = []string{integrationTestPAT}
	}
	h.Routes(r, tokens)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, v
}

const integrationTestPAT = "integration-test-pat"

// Test: provisioning stores project credentials, auth reads them
func TestIntegration_ProvisioningStoresCredentials_AuthReads(t *testing.T) {
	pat := "platform-service-token"
	ts, _ := setupIntegrationServer(t, []string{pat})

	// Platform (provisioning) client writes credentials
	platformClient := NewHTTPClient(ts.URL, pat)

	err := platformClient.Put("projects/duc-corp/app-a/credentials/auth_admin", map[string]string{
		"host":     testVaultHost,
		"port":     "5432",
		"username": "auth_admin",
		"password": "auth-secret-123",
		"database": "app",
	})
	if err != nil {
		t.Fatalf("platform Put credentials: %v", err)
	}

	err = platformClient.Put("projects/duc-corp/app-a/credentials/excalibase_app", map[string]string{
		"host":     testVaultHost,
		"port":     "5432",
		"username": "excalibase_app",
		"password": "app-secret-456",
		"database": "app",
	})
	if err != nil {
		t.Fatalf("platform Put app credentials: %v", err)
	}

	// Auth service client reads auth_admin credentials
	authClient := NewHTTPClient(ts.URL, pat)
	authCreds, err := authClient.Get("projects/duc-corp/app-a/credentials/auth_admin")
	if err != nil {
		t.Fatalf("auth Get credentials: %v", err)
	}
	if authCreds["username"] != "auth_admin" {
		t.Errorf("auth expected username=auth_admin, got %s", authCreds["username"])
	}
	if authCreds["password"] != "auth-secret-123" {
		t.Errorf("auth expected password=auth-secret-123, got %s", authCreds["password"])
	}

	// GraphQL service client reads excalibase_app credentials
	graphqlClient := NewHTTPClient(ts.URL, pat)
	appCreds, err := graphqlClient.Get("projects/duc-corp/app-a/credentials/excalibase_app")
	if err != nil {
		t.Fatalf("graphql Get credentials: %v", err)
	}
	if appCreds["username"] != "excalibase_app" {
		t.Errorf("graphql expected username=excalibase_app, got %s", appCreds["username"])
	}
	if appCreds["host"] != testVaultHost {
		t.Errorf("graphql expected correct host, got %s", appCreds["host"])
	}
}

// Test: PKI signing key flow (auth fetches private key)
func TestIntegration_PKISigningKeyFlow(t *testing.T) {
	pat := "service-token"
	ts, _ := setupIntegrationServer(t, []string{pat})
	client := NewHTTPClient(ts.URL, pat)

	// Store a signing key (normally done by vault init/PKI bootstrap)
	err := client.Put("pki/signing/private", map[string]string{
		"key": "-----BEGIN EC PRIVATE KEY-----\nfake-key-data\n-----END EC PRIVATE KEY-----",
	})
	if err != nil {
		t.Fatalf("Put signing key: %v", err)
	}

	// Auth service reads it back
	data, err := client.Get("pki/signing/private")
	if err != nil {
		t.Fatalf("Get signing key: %v", err)
	}
	if data["key"] == "" {
		t.Error("expected non-empty signing key")
	}
}

// Test: multi-project isolation — credentials for project A not visible as project B
func TestIntegration_MultiProjectIsolation(t *testing.T) {
	ts, _ := setupIntegrationServer(t, nil)
	client := NewHTTPClient(ts.URL, integrationTestPAT)

	// Store creds for two different projects
	client.Put(testVaultCredsPath, map[string]string{"password": "a-secret"})
	client.Put("projects/org-b/app-b/credentials/admin", map[string]string{"password": "b-secret"})

	// Read each — verify isolation
	credsA, err := client.Get(testVaultCredsPath)
	if err != nil {
		t.Fatalf("Get project A: %v", err)
	}
	credsB, err := client.Get("projects/org-b/app-b/credentials/admin")
	if err != nil {
		t.Fatalf("Get project B: %v", err)
	}

	if credsA["password"] != "a-secret" {
		t.Errorf("project A password wrong: %s", credsA["password"])
	}
	if credsB["password"] != "b-secret" {
		t.Errorf("project B password wrong: %s", credsB["password"])
	}

	// Non-existent project returns error
	_, err = client.Get("projects/org-c/app-c/credentials/admin")
	if err == nil {
		t.Error("expected error for non-existent project")
	}
}

// Test: credential rotation — overwrite existing secret
func TestIntegration_CredentialRotation(t *testing.T) {
	ts, _ := setupIntegrationServer(t, nil)
	client := NewHTTPClient(ts.URL, integrationTestPAT)

	path := testVaultCredsPath

	// Store initial credentials
	client.Put(path, map[string]string{"password": "old-password", "host": "old-host"})

	// Rotate (overwrite)
	client.Put(path, map[string]string{"password": "new-password", "host": "new-host"})

	// Verify new credentials
	data, err := client.Get(path)
	if err != nil {
		t.Fatalf("Get after rotation: %v", err)
	}
	if data["password"] != "new-password" {
		t.Errorf("expected new-password, got %s", data["password"])
	}
	if data["host"] != "new-host" {
		t.Errorf("expected new-host, got %s", data["host"])
	}
}

// Test: vault seal blocks reads and writes
func TestIntegration_SealBlocksOperations(t *testing.T) {
	ts, v := setupIntegrationServer(t, nil)
	client := NewHTTPClient(ts.URL, integrationTestPAT)

	// Store a secret while unsealed
	client.Put(testIntegSecretPath, map[string]string{"key": "value"})

	// Seal vault
	v.Seal()

	// All operations should fail
	_, err := client.Get(testIntegSecretPath)
	if err == nil {
		t.Error("Get should fail when sealed")
	}

	err = client.Put("test/new", map[string]string{"key": "value"})
	if err == nil {
		t.Error("Put should fail when sealed")
	}

	err = client.Delete(testIntegSecretPath)
	if err == nil {
		t.Error("Delete should fail when sealed")
	}
}

// Test: delete removes credential permanently
func TestIntegration_DeleteCredentials(t *testing.T) {
	ts, _ := setupIntegrationServer(t, nil)
	client := NewHTTPClient(ts.URL, integrationTestPAT)

	path := "projects/org-a/deleted-app/credentials/admin"
	client.Put(path, map[string]string{"password": "will-be-deleted"})

	// Verify exists
	data, err := client.Get(path)
	if err != nil {
		t.Fatalf("should exist before delete: %v", err)
	}
	if data["password"] != "will-be-deleted" {
		t.Errorf("wrong password: %s", data["password"])
	}

	// Delete
	if err := client.Delete(path); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify gone
	_, err = client.Get(path)
	if err == nil {
		t.Error("should not exist after delete")
	}
}
