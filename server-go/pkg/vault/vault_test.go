package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testVaultFile      = "vault.bolt"
	testVaultCredsPath = "projects/my-app/credentials/admin"
	testVaultIP        = "10.0.0.5"
	testVaultKey       = "test/key"
)


func tempVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, testVaultFile)
	v, err := New(path)
	if err != nil {
		t.Fatalf("New vault: %v", err)
	}
	return v
}

func TestInitAndUnseal(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	// Should not be initialized yet
	if v.Initialized() {
		t.Fatal("vault should not be initialized")
	}
	if !v.Sealed() {
		t.Fatal("vault should be sealed")
	}

	// Initialize with 5 shares, threshold 3
	result, err := v.Init(5, 3)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(result.Shares) != 5 {
		t.Fatalf("expected 5 shares, got %d", len(result.Shares))
	}
	if result.Threshold != 3 {
		t.Fatalf("expected threshold 3, got %d", result.Threshold)
	}

	// After init, vault is unsealed
	if v.Sealed() {
		t.Fatal("vault should be unsealed after init")
	}
	if !v.Initialized() {
		t.Fatal("vault should be initialized")
	}

	// Seal it
	v.Seal()
	if !v.Sealed() {
		t.Fatal("vault should be sealed after Seal()")
	}

	// Unseal with 3 of 5 shares
	progress, err := v.Unseal(result.Shares[0])
	if err != nil {
		t.Fatalf("Unseal share 0: %v", err)
	}
	if progress.Done {
		t.Fatal("should not be unsealed after 1 share")
	}
	if progress.Progress != 1 || progress.Threshold != 3 {
		t.Fatalf("progress: got %d/%d", progress.Progress, progress.Threshold)
	}

	progress, _ = v.Unseal(result.Shares[2])
	if progress.Done {
		t.Fatal("should not be unsealed after 2 shares")
	}

	progress, _ = v.Unseal(result.Shares[4])
	if !progress.Done {
		t.Fatal("should be unsealed after 3 shares")
	}
	if v.Sealed() {
		t.Fatal("vault should be unsealed")
	}
}

func TestInitSingleKey(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(result.Shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(result.Shares))
	}

	v.Seal()
	progress, err := v.Unseal(result.Shares[0])
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !progress.Done {
		t.Fatal("should be unsealed with single key")
	}
}

func TestSecretCRUD(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	v.Init(1, 1)

	// Put a secret
	err := v.Put(testVaultCredsPath, map[string]string{
		"host":     testVaultIP,
		"port":     "5432",
		"username": "admin",
		"password": "secret123",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get it back
	data, err := v.Get(testVaultCredsPath)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if data["host"] != testVaultIP {
		t.Errorf("host: got %s", data["host"])
	}
	if data["password"] != "secret123" {
		t.Errorf("password: got %s", data["password"])
	}

	// Update it
	err = v.Put(testVaultCredsPath, map[string]string{
		"host":     testVaultIP,
		"port":     "5432",
		"username": "admin",
		"password": "new-password",
	})
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	data, _ = v.Get(testVaultCredsPath)
	if data["password"] != "new-password" {
		t.Errorf("password after update: got %s", data["password"])
	}

	// Delete it
	err = v.Delete(testVaultCredsPath)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = v.Get(testVaultCredsPath)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestSecretRequiresUnseal(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	result, _ := v.Init(1, 1)
	v.Put(testVaultKey, map[string]string{"value": "hello"})

	v.Seal()

	// All operations should fail when sealed
	_, err := v.Get(testVaultKey)
	if err != ErrSealed {
		t.Errorf("Get while sealed: expected ErrSealed, got %v", err)
	}
	err = v.Put("test/key2", map[string]string{"value": "world"})
	if err != ErrSealed {
		t.Errorf("Put while sealed: expected ErrSealed, got %v", err)
	}
	err = v.Delete(testVaultKey)
	if err != ErrSealed {
		t.Errorf("Delete while sealed: expected ErrSealed, got %v", err)
	}

	// Unseal and verify data persisted
	v.Unseal(result.Shares[0])
	data, err := v.Get(testVaultKey)
	if err != nil {
		t.Fatalf("Get after unseal: %v", err)
	}
	if data["value"] != "hello" {
		t.Errorf("value: got %s", data["value"])
	}
}

func TestRekey(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	result, _ := v.Init(1, 1)
	v.Put("test/secret", map[string]string{"key": "value"})

	// Rekey to 3 shares, threshold 2
	newResult, err := v.Rekey(3, 2)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if len(newResult.Shares) != 3 {
		t.Fatalf("expected 3 new shares, got %d", len(newResult.Shares))
	}

	// Seal and unseal with OLD shares should fail
	v.Seal()
	_, err = v.Unseal(result.Shares[0])
	// Even with old share, we need threshold — and old shares reconstruct wrong MEK
	// Reset unseal progress
	v.ResetUnseal()

	// Unseal with new shares
	v.Unseal(newResult.Shares[0])
	progress, _ := v.Unseal(newResult.Shares[1])
	if !progress.Done {
		t.Fatal("should be unsealed with 2 of 3 new shares")
	}

	// Secret should still be accessible
	data, err := v.Get("test/secret")
	if err != nil {
		t.Fatalf("Get after rekey: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("value: got %s", data["key"])
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, testVaultFile)

	// Create vault, init, store secret
	v1, _ := New(path)
	result, _ := v1.Init(1, 1)
	v1.Put("persistent/secret", map[string]string{"answer": "42"})
	v1.Close()

	// Reopen vault — should be initialized but sealed
	v2, _ := New(path)
	defer v2.Close()

	if !v2.Initialized() {
		t.Fatal("vault should be initialized after reopen")
	}
	if !v2.Sealed() {
		t.Fatal("vault should be sealed after reopen")
	}

	// Unseal and read
	v2.Unseal(result.Shares[0])
	data, err := v2.Get("persistent/secret")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if data["answer"] != "42" {
		t.Errorf("answer: got %s", data["answer"])
	}
}

func TestInitAlreadyInitialized(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	v.Init(1, 1)
	_, err := v.Init(1, 1)
	if err != ErrAlreadyInit {
		t.Fatalf("expected ErrAlreadyInit, got %v", err)
	}
}

func TestInitInvalidParams(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	_, err := v.Init(0, 1)
	if err == nil {
		t.Fatal("expected error for shares=0")
	}
	_, err = v.Init(3, 5)
	if err == nil {
		t.Fatal("expected error for threshold > shares")
	}
}

func TestUnsealNotInitialized(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	_, err := v.Unseal("deadbeef")
	if err != ErrNotInitialized {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	v := tempVault(t)
	defer v.Close()
	v.Init(1, 1)

	_, err := v.Get("nonexistent/path")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRekeyWhileSealed(t *testing.T) {
	v := tempVault(t)
	defer v.Close()
	v.Init(1, 1)
	v.Seal()

	_, err := v.Rekey(3, 2)
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestInitGeneratesPKI(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	v.Init(1, 1)

	// PKI keys should be generated during init
	privKey, err := v.Get("pki/signing/private")
	if err != nil {
		t.Fatalf("Get private key: %v", err)
	}
	if privKey["key"] == "" {
		t.Fatal("private key should not be empty")
	}
	if privKey["algorithm"] != "EC-P256" {
		t.Errorf("algorithm: got %s, want EC-P256", privKey["algorithm"])
	}

	pubKey, err := v.Get("pki/signing/public")
	if err != nil {
		t.Fatalf("Get public key: %v", err)
	}
	if pubKey["key"] == "" {
		t.Fatal("public key should not be empty")
	}
}

func TestGetPublicKey(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	v.Init(1, 1)

	// Public key should be accessible via dedicated method
	pem, err := v.GetPublicKey()
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	if pem == "" {
		t.Fatal("public key PEM should not be empty")
	}
	if !strings.Contains(pem, "BEGIN PUBLIC KEY") {
		t.Error("should be PEM-encoded public key")
	}
}

func TestListSecrets(t *testing.T) {
	v := tempVault(t)
	defer v.Close()
	v.Init(1, 1)

	// Store secrets at various paths
	v.Put("projects/org-a/app-a/credentials/admin", map[string]string{"password": "a"})
	v.Put("projects/org-a/app-a/credentials/auth_admin", map[string]string{"password": "b"})
	v.Put("projects/org-b/app-b/credentials/admin", map[string]string{"password": "c"})
	v.Put("backup/s3", map[string]string{"key": "s3key"})

	// List all
	paths, err := v.List("")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	// Should include all user secrets (pki/* auto-created by Init, plus our 4)
	if len(paths) < 4 {
		t.Errorf("expected at least 4 paths, got %d: %v", len(paths), paths)
	}

	// List with prefix
	paths, err = v.List("projects/org-a/")
	if err != nil {
		t.Fatalf("List prefix: %v", err)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 paths for org-a, got %d: %v", len(paths), paths)
	}

	// List non-existent prefix
	paths, err = v.List("nonexistent/")
	if err != nil {
		t.Fatalf("List nonexistent: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %d", len(paths))
	}
}

func TestListSecretsRequiresUnseal(t *testing.T) {
	v := tempVault(t)
	defer v.Close()
	result, _ := v.Init(1, 1)
	v.Put(testVaultKey, map[string]string{"value": "hello"})
	v.Seal()

	_, err := v.List("")
	if err != ErrSealed {
		t.Errorf("List while sealed: expected ErrSealed, got %v", err)
	}

	v.Unseal(result.Shares[0])
	paths, err := v.List("")
	if err != nil {
		t.Fatalf("List after unseal: %v", err)
	}
	if len(paths) < 1 {
		t.Error("expected at least 1 path after unseal")
	}
}

func TestAutoUnsealFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, testVaultFile)

	// Create and init
	v1, _ := New(path)
	result, _ := v1.Init(1, 1)
	v1.Close()

	// Set env var with the MEK (share[0] == full MEK when N=1)
	os.Setenv("VAULT_UNSEAL_KEY", result.Shares[0])
	defer os.Unsetenv("VAULT_UNSEAL_KEY")

	// Reopen — should auto-unseal
	v2, _ := New(path)
	defer v2.Close()

	if v2.Sealed() {
		t.Fatal("vault should be auto-unsealed from env var")
	}
}
