package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// MFAKeyField is the vault field holding the project's MFA key.
const MFAKeyField = "key"

const mfaKeyBytes = 32

// ErrInvalidMFAKey means the vault holds something at the MFA key path that
// is not a 32-byte key. It is never replaced: factors may be sealed with it.
var ErrInvalidMFAKey = errors.New("the project's MFA key in the vault is not a valid key; it was left as it is and MFA stays off")

// ErrMFAKeyLost means the vault no longer holds the key the project's factors
// were sealed with: a new one would lock every enrolled user out.
var ErrMFAKeyLost = errors.New("the project's MFA key is missing from the vault or is not the key its users' factors were sealed with; restore it from a vault backup, MFA stays off")

// MFAKeyFingerprint names a key by its SHA-256, so the platform database can
// tell which key sealed the factors without holding it.
func MFAKeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// MFAKeyPath is the vault path of the key that seals the project's TOTP
// secrets, under projects/{id}/ so deleting the project purges it.
func MFAKeyPath(projectID string) string {
	return "projects/" + projectID + "/auth/mfa"
}

// NewMFAKey returns 32 bytes from crypto/rand in standard base64.
func NewMFAKey() (string, error) {
	raw := make([]byte, mfaKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ValidMFAKey reports whether key is standard base64 of exactly 32 bytes.
func ValidMFAKey(key string) bool {
	raw, err := base64.StdEncoding.Strict().DecodeString(key)
	return err == nil && len(raw) == mfaKeyBytes
}
