package natsauth

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

// passwordBytes is the entropy behind a minted credential. 32 bytes is well
// past bcrypt's useful input and leaves no room for guessing.
const passwordBytes = 32

// NewPassword mints a fresh credential. The plaintext is returned once — it
// is delivered to the principal (a k8s Secret) and never persisted here.
func NewPassword() (string, error) {
	buf := make([]byte, passwordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashPassword produces the value stored in nats_credentials.password_hash.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
