package appregistry

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenIssuer is the iss claim; zot checks the signature, the clock and the access claim.
const tokenIssuer = "excalibase-provisioning"

const clockSkew = time.Minute

// MaxTokenTTL bounds every token: the longest build the admin bounds allow.
const MaxTokenTTL = 2 * time.Hour

// Grant is one entry of the Docker token spec's access claim.
type Grant struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// Issuer signs registry tokens with the key whose public half zot verifies.
type Issuer struct {
	key     crypto.Signer
	method  jwt.SigningMethod
	service string
	pullKey []byte
	now     func() time.Time
}

// NewIssuer reads a PEM private key (PKCS#1 RSA, or PKCS#8 RSA, EC P-256 or
// Ed25519) for tokens addressed to service, the registry's own name.
func NewIssuer(keyPEM []byte, service string) (*Issuer, error) {
	if service == "" {
		return nil, errors.New("the registry token service is empty")
	}
	key, err := parseSigningKey(keyPEM)
	if err != nil {
		return nil, err
	}
	method, err := methodFor(key)
	if err != nil {
		return nil, err
	}
	der, err := privateKeyDER(key)
	if err != nil {
		return nil, fmt.Errorf("the registry token key: %w", err)
	}
	return &Issuer{key: key, method: method, service: service, pullKey: derivePullKey(der), now: time.Now}, nil
}

func parseSigningKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("the registry token key is not PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the registry token key: %w", err)
		}
		signer, ok := parsed.(crypto.Signer)
		if !ok {
			return nil, errors.New("the registry token key cannot sign")
		}
		return signer, nil
	}
	return nil, fmt.Errorf("the registry token key is a %q, not a private key", block.Type)
}

func methodFor(key crypto.Signer) (jwt.SigningMethod, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		if k.N.BitLen() < 2048 {
			return nil, errors.New("the registry token key is RSA under 2048 bits")
		}
		return jwt.SigningMethodRS256, nil
	case *ecdsa.PrivateKey:
		if k.Curve.Params().BitSize == 256 {
			return jwt.SigningMethodES256, nil
		}
	case ed25519.PrivateKey:
		return jwt.SigningMethodEdDSA, nil
	}
	return nil, errors.New("the registry token key is not RSA, EC P-256 or Ed25519")
}

// Mint signs a token for subject with exactly the grants given; the caller has already decided they are the subject's.
func (i *Issuer) Mint(subject string, grants []Grant, ttl time.Duration) (string, error) {
	if subject == "" || ttl <= 0 || ttl > MaxTokenTTL {
		return "", fmt.Errorf("a registry token needs a subject and a lifetime up to %s", MaxTokenTTL)
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	// iat and nbf sit a minute back, so zot's clock running behind ours does not refuse a fresh token.
	now := i.now()
	claims := struct {
		jwt.RegisteredClaims
		Access []Grant `json:"access"`
	}{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: tokenIssuer, Subject: subject, Audience: jwt.ClaimStrings{i.service}, ID: hex.EncodeToString(id),
			IssuedAt: jwt.NewNumericDate(now.Add(-clockSkew)), NotBefore: jwt.NewNumericDate(now.Add(-clockSkew)), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Access: grantsOrEmpty(grants),
	}
	return jwt.NewWithClaims(i.method, claims).SignedString(i.key)
}

// grantsOrEmpty keeps the access claim an array: zot reads a null as no access, but clients expect [].
func grantsOrEmpty(grants []Grant) []Grant {
	if grants == nil {
		return []Grant{}
	}
	return grants
}
