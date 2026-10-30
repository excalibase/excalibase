package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// MFAKeyField is the vault field holding the project's MFA key.
const MFAKeyField = "key"

const mfaKeyBytes = 32

// ErrInvalidMFAKey means the vault holds something at the MFA key path that
// is not a 32-byte key. It is never replaced: factors may be sealed with it.
var ErrInvalidMFAKey = errors.New("the project's MFA key in the vault is not a valid key; it was left as it is and MFA stays off")

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
