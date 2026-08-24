package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// EnsureReady must make a fresh vault usable with no operator steps (the
// selfhosted/docker happy path): init on first run, persist the generated key,
// and unseal from that persisted key after a restart.
func TestEnsureReady_FirstRunInitsAndPersists(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "unseal.key")
	v, err := New(filepath.Join(dir, testVaultFile))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := EnsureReady(v, keyPath, ""); err != nil {
		t.Fatalf("EnsureReady first run: %v", err)
	}
	if !v.Initialized() || v.Sealed() {
		t.Fatalf("after first run want initialized+unsealed, got initialized=%v sealed=%v", v.Initialized(), v.Sealed())
	}
	// Key was persisted with 0600 for the restart path.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("unseal key not persisted: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("unseal key perm = %o, want 600", perm)
	}
}

// A restart re-opens the same bbolt file (initialized but sealed) and must
// auto-unseal from the persisted key.
func TestEnsureReady_RestartUnsealsFromFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "unseal.key")
	vaultPath := filepath.Join(dir, testVaultFile)

	v1, _ := New(vaultPath)
	if err := EnsureReady(v1, keyPath, ""); err != nil {
		t.Fatalf("first run: %v", err)
	}
	v1.Close()

	// Simulate restart: same file, fresh handle → initialized but sealed.
	v2, err := New(vaultPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !v2.Initialized() || !v2.Sealed() {
		t.Fatalf("reopened vault want initialized+sealed, got initialized=%v sealed=%v", v2.Initialized(), v2.Sealed())
	}
	if err := EnsureReady(v2, keyPath, ""); err != nil {
		t.Fatalf("restart EnsureReady: %v", err)
	}
	if v2.Sealed() {
		t.Error("restart must auto-unseal from the persisted key")
	}
}

// An operator-supplied env key is used instead of the file (and no key file is
// written on first init when the operator holds the key).
func TestEnsureReady_RestartUnsealsFromEnvKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "unseal.key")
	vaultPath := filepath.Join(dir, testVaultFile)

	// First init WITHOUT persisting to file: operator will hold the key.
	v1, _ := New(vaultPath)
	res, err := v1.Init(1, 1)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	envKey := res.Shares[0]
	v1.Close()

	v2, _ := New(vaultPath)
	if err := EnsureReady(v2, keyPath, envKey); err != nil {
		t.Fatalf("EnsureReady with env key: %v", err)
	}
	if v2.Sealed() {
		t.Error("must unseal from the operator-supplied env key")
	}
}

// Sealed with no key available anywhere → clear error, not a silent bad state.
func TestEnsureReady_SealedNoKeyErrors(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, testVaultFile)
	v1, _ := New(vaultPath)
	if _, err := v1.Init(1, 1); err != nil {
		t.Fatalf("init: %v", err)
	}
	v1.Close()

	v2, _ := New(vaultPath)
	// No env key, no key file → cannot unseal.
	if err := EnsureReady(v2, filepath.Join(dir, "missing.key"), ""); err == nil {
		t.Error("expected an error when sealed with no unseal key available")
	}
}
