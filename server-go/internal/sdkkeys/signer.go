// Package sdkkeys manages a project's SDK api keys through excalibase-auth,
// which alone mints and stores them. The control plane authenticates to auth
// with a short key-admin token signed by the platform key it already holds;
// the browser never sees that credential.
package sdkkeys

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	signingKeyPath = "pki/signing/private"
	// These must match excalibase-auth: its key routes accept exactly this
	// use and audience, and the engine's "excalibase:" audience check and
	// access-only token_use check both refuse it.
	tokenUseKeyAdmin       = "key_admin"
	keyAdminAudiencePrefix = "excalibase-auth:"
	keyAdminIssuer         = "excalibase"
	keyAdminSubject        = "svc-provisioning"
	keyAdminLifetime       = 60 * time.Second
)

type secretReader interface {
	Get(path string) (map[string]string, error)
}

// Signer mints key-admin tokens with the platform signing key.
type Signer struct {
	vault secretReader
	now   func() time.Time
}

func NewSigner(vault secretReader) *Signer {
	return &Signer{vault: vault, now: time.Now}
}

// Sign returns a token that lets its bearer manage projectID's api keys at
// the auth service for one minute.
func (s *Signer) Sign(projectID, orgSlug string) (string, error) {
	key, err := s.signingKey()
	if err != nil {
		return "", err
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("token id: %w", err)
	}
	now := s.now()
	return jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":       keyAdminIssuer,
		"sub":       keyAdminSubject,
		"aud":       []string{keyAdminAudiencePrefix + projectID},
		"projectId": projectID,
		"orgSlug":   orgSlug,
		"token_use": tokenUseKeyAdmin,
		"jti":       hex.EncodeToString(jti),
		"iat":       now.Unix(),
		"exp":       now.Add(keyAdminLifetime).Unix(),
	}).SignedString(key)
}

func (s *Signer) signingKey() (*ecdsa.PrivateKey, error) {
	data, err := s.vault.Get(signingKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read platform signing key: %w", err)
	}
	block, _ := pem.Decode([]byte(data["key"]))
	if block == nil {
		return nil, errors.New("platform signing key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse platform signing key: %w", err)
	}
	return key, nil
}
