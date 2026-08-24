package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keySize = 32 // AES-256
const ivSize = 12  // GCM standard nonce

// Encryptor handles AES-256-GCM encryption for credential storage.
type Encryptor struct {
	key []byte
}

// NewEncryptor loads or generates the encryption key from the given directory.
func NewEncryptor(storagePath string) (*Encryptor, error) {
	keyFile := filepath.Join(storagePath, ".encryption-key")
	key, err := os.ReadFile(keyFile)
	if err == nil {
		decoded, err := base64.StdEncoding.DecodeString(string(key))
		if err != nil {
			return nil, fmt.Errorf("decode key: %w", err)
		}
		return &Encryptor{key: decoded}, nil
	}

	// Generate new key
	newKey := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(newKey)
	if err := os.WriteFile(keyFile, []byte(encoded), 0600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}

	return &Encryptor{key: newKey}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM. Returns base64-encoded [IV + ciphertext + tag].
func (e *Encryptor) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, ivSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil) // prepends nonce
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts base64-encoded [IV + ciphertext + tag] using AES-256-GCM.
func (e *Encryptor) Decrypt(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	if len(data) < ivSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}

	nonce := data[:ivSize]
	ciphertext := data[ivSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}
