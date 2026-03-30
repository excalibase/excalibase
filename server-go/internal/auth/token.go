package auth

import (
	"crypto/rand"
	"crypto/sha256"
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

// TokenPrefix returns the first 12 chars for display.
func TokenPrefix(raw string) string {
	if len(raw) < 12 {
		return raw
	}
	return raw[:12]
}
