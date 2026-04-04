package security

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// TestNewEncryptorCorruptedKeyFile covers the branch where the key file exists
// but contains invalid base64, triggering the decode error path.
func TestNewEncryptorCorruptedKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, ".encryption-key")

	if err := os.WriteFile(keyFile, []byte("!!!not-valid-base64!!!"), 0600); err != nil {
		t.Fatalf("write corrupted key: %v", err)
	}

	_, err := NewEncryptor(dir)
	if err == nil {
		t.Fatal("expected error for corrupted key file, got nil")
	}
}

// TestNewEncryptorWriteProtectedDir covers the WriteFile error path by making
// the storage directory read-only after MkdirAll would succeed but before the
// key write. We achieve this by using a path whose parent is read-only.
func TestNewEncryptorWriteProtectedDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}

	parent := t.TempDir()
	// Make the parent read-only so writing the key file inside it fails.
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0700) })

	// Use a subdirectory inside the read-only parent — MkdirAll will fail.
	storagePath := filepath.Join(parent, "subdir")
	_, err := NewEncryptor(storagePath)
	if err == nil {
		t.Fatal("expected error when storage directory is not writable, got nil")
	}
}

// TestEncryptBadKey covers the aes.NewCipher error inside Encrypt by
// constructing an Encryptor directly with an invalid key length.
func TestEncryptBadKey(t *testing.T) {
	enc := &Encryptor{key: []byte("tooshort")} // AES requires 16/24/32 bytes

	_, err := enc.Encrypt([]byte("hello"))
	if err == nil {
		t.Fatal("expected error for bad key in Encrypt, got nil")
	}
}

// TestDecryptBadKey covers the aes.NewCipher error inside Decrypt by
// constructing an Encryptor directly with an invalid key length.
func TestDecryptBadKey(t *testing.T) {
	// Produce data that passes the base64 decode and len(data) >= ivSize checks
	// but then hits the aes.NewCipher call with an invalid key.
	payload := make([]byte, ivSize+16) // enough bytes to pass length guard
	encoded := base64.StdEncoding.EncodeToString(payload)

	enc := &Encryptor{key: []byte("tooshort")}
	_, err := enc.Decrypt(encoded)
	if err == nil {
		t.Fatal("expected error for bad key in Decrypt, got nil")
	}
}

// TestDecryptTamperedCiphertext covers gcm.Open failure (authentication tag
// mismatch) when the ciphertext has been tampered with.
func TestDecryptTamperedCiphertext(t *testing.T) {
	dir := t.TempDir()
	enc, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	ct, err := enc.Encrypt([]byte("sensitive"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Decode, flip a byte in the ciphertext body, re-encode.
	raw, _ := base64.StdEncoding.DecodeString(ct)
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)

	_, err = enc.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext, got nil")
	}
}

// TestEncryptProducesUniqueNonces checks that two encryptions of the same
// plaintext produce different ciphertexts (probabilistic nonce uniqueness).
func TestEncryptProducesUniqueNonces(t *testing.T) {
	dir := t.TempDir()
	enc, _ := NewEncryptor(dir)

	ct1, _ := enc.Encrypt([]byte("same plaintext"))
	ct2, _ := enc.Encrypt([]byte("same plaintext"))

	if ct1 == ct2 {
		t.Error("two encryptions of identical plaintext should not produce identical output")
	}
}

// TestEncryptDecryptEmptyPlaintext verifies the round-trip for an empty byte slice.
func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	dir := t.TempDir()
	enc, _ := NewEncryptor(dir)

	ct, err := enc.Encrypt([]byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	pt, err := enc.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("expected empty plaintext, got %q", pt)
	}
}

// TestDecryptExactlyIVSizeData covers the boundary case where len(data) == ivSize,
// which should still fail because there is no ciphertext body.
func TestDecryptExactlyIVSizeData(t *testing.T) {
	dir := t.TempDir()
	enc, _ := NewEncryptor(dir)

	// ivSize bytes encoded to base64 passes the len(data) < ivSize guard
	// but GCM Open will fail because there is no ciphertext.
	payload := make([]byte, ivSize)
	encoded := base64.StdEncoding.EncodeToString(payload)

	_, err := enc.Decrypt(encoded)
	if err == nil {
		t.Fatal("expected error when ciphertext body is empty, got nil")
	}
}
