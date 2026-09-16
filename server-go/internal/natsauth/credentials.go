package natsauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// errDenied is the single error every authentication failure returns, so a
// caller (and anything that logs it) cannot tell an unknown principal from a
// wrong password from an unreachable store.
var errDenied = errors.New("nats credential rejected")

// passwordBytes is the entropy behind a minted credential. 32 bytes is well
// past bcrypt's useful input and leaves no room for guessing.
const passwordBytes = 32

// CredentialStore is the minimum the callout needs from persistence: the
// stored bcrypt hash for a principal, if one exists.
type CredentialStore interface {
	LookupNatsCredentialHash(ctx context.Context, principal string) (string, bool, error)
}

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

// Authenticate verifies a principal's password against the stored hash. It
// fails closed: a missing store, a store error, an unknown principal and a
// wrong password are all indistinguishable rejections.
func Authenticate(ctx context.Context, store CredentialStore, principal, password string) error {
	if store == nil || password == "" {
		return errDenied
	}
	hash, found, err := store.LookupNatsCredentialHash(ctx, principal)
	if err != nil || !found {
		return errDenied
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return errDenied
	}
	return nil
}
