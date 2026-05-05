package security

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	enc, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	plaintext := []byte(fmt.Sprintf(`{"password":%q,"host":"db.local"}`, testutil.FixtureSecret("enc-test")))
	encrypted, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if encrypted == string(plaintext) {
		t.Error("encrypted should differ from plaintext")
	}

	decrypted, err := enc.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("got %s, want %s", decrypted, plaintext)
	}
}

func TestEncryptorPersistsKey(t *testing.T) {
	dir := t.TempDir()

	enc1, _ := NewEncryptor(dir)
	encrypted, _ := enc1.Encrypt([]byte("hello"))

	// Reload from disk
	enc2, _ := NewEncryptor(dir)
	decrypted, err := enc2.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt with reloaded key: %v", err)
	}
	if string(decrypted) != "hello" {
		t.Errorf("got %s, want hello", decrypted)
	}
}

func TestKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	NewEncryptor(dir)

	info, _ := os.Stat(filepath.Join(dir, ".encryption-key"))
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("key file permissions: got %o, want 600", perm)
	}
}

func TestDecryptInvalidData(t *testing.T) {
	dir := t.TempDir()
	enc, _ := NewEncryptor(dir)

	_, err := enc.Decrypt("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}

	_, err = enc.Decrypt("dG9vc2hvcnQ=") // "tooshort" base64
	if err == nil {
		t.Error("expected error for short ciphertext")
	}
}
