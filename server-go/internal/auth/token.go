package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// GenerateToken creates a random PAT like "excb_a1b2c3d4..."
func GenerateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("excb_%s", hex.EncodeToString(b))
}

// HashToken returns SHA-256 hex digest of the raw token.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// GenerateSetupToken creates a random one-time token for the first-admin
// setup flow (EXC-451): 32 random bytes, URL-safe base64 so it can be pasted
// into a link or a form field without escaping.
func GenerateSetupToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenPrefix returns the first 12 chars for display.
func TokenPrefix(raw string) string {
	if len(raw) < 12 {
		return raw
	}
	return raw[:12]
}
