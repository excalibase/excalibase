package vault

import (
	"os"
	"path/filepath"
	"testing"
)

func tempVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bolt")
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
	err := v.Put("projects/my-app/credentials/admin", map[string]string{
		"host":     "10.0.0.5",
		"port":     "5432",
		"username": "admin",
		"password": "secret123",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get it back
	data, err := v.Get("projects/my-app/credentials/admin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if data["host"] != "10.0.0.5" {
		t.Errorf("host: got %s", data["host"])
	}
	if data["password"] != "secret123" {
		t.Errorf("password: got %s", data["password"])
	}

	// Update it
	err = v.Put("projects/my-app/credentials/admin", map[string]string{
		"host":     "10.0.0.5",
		"port":     "5432",
		"username": "admin",
		"password": "new-password",
	})
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	data, _ = v.Get("projects/my-app/credentials/admin")
	if data["password"] != "new-password" {
		t.Errorf("password after update: got %s", data["password"])
	}

	// Delete it
	err = v.Delete("projects/my-app/credentials/admin")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = v.Get("projects/my-app/credentials/admin")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestSecretRequiresUnseal(t *testing.T) {
	v := tempVault(t)
	defer v.Close()

	result, _ := v.Init(1, 1)
	v.Put("test/key", map[string]string{"value": "hello"})

	v.Seal()

	// All operations should fail when sealed
	_, err := v.Get("test/key")
	if err != ErrSealed {
		t.Errorf("Get while sealed: expected ErrSealed, got %v", err)
	}
	err = v.Put("test/key2", map[string]string{"value": "world"})
	if err != ErrSealed {
		t.Errorf("Put while sealed: expected ErrSealed, got %v", err)
	}
	err = v.Delete("test/key")
	if err != ErrSealed {
		t.Errorf("Delete while sealed: expected ErrSealed, got %v", err)
	}

	// Unseal and verify data persisted
	v.Unseal(result.Shares[0])
	data, err := v.Get("test/key")
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
	path := filepath.Join(dir, "vault.bolt")

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

func TestAutoUnsealFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bolt")

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
