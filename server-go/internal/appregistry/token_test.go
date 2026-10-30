package appregistry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func rsaKeyPEM(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), key
}

func testIssuer(t *testing.T) (*Issuer, *rsa.PrivateKey) {
	t.Helper()
	raw, key := rsaKeyPEM(t)
	issuer, err := NewIssuer(raw, "registry.excalibase.internal")
	if err != nil {
		t.Fatal(err)
	}
	return issuer, key
}

type parsedClaims struct {
	jwt.RegisteredClaims
	Access []Grant `json:"access"`
}

func TestIssuer_MintsATokenZotVerifies(t *testing.T) {
	issuer, key := testIssuer(t)
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	issuer.now = func() time.Time { return at }
	token, err := issuer.Mint("proj-a", []Grant{{Type: "repository", Name: "proj-a/api", Actions: []string{"pull", "push"}}}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var claims parsedClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(func() time.Time { return at.Add(time.Minute) }), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid {
		t.Fatalf("verify: %v", err)
	}
	expectAll(t, map[string]bool{
		"subject":          claims.Subject == "proj-a",
		"audience":         len(claims.Audience) == 1 && claims.Audience[0] == "registry.excalibase.internal",
		"issuer":           claims.Issuer == tokenIssuer,
		"expires with ttl": claims.ExpiresAt.Time.Equal(at.Add(15 * time.Minute)),
		"the grant":        len(claims.Access) == 1 && claims.Access[0].Name == "proj-a/api" && strings.Join(claims.Access[0].Actions, ",") == "pull,push",
		"an id":            claims.ID != "",
	})
}

func TestIssuer_RefusesALongTTLAndAnEmptySubject(t *testing.T) {
	issuer, _ := testIssuer(t)
	if _, err := issuer.Mint("proj-a", nil, 3*time.Hour); err == nil {
		t.Error("a token past the longest build")
	}
	if _, err := issuer.Mint("", nil, time.Minute); err == nil {
		t.Error("a token with no subject")
	}
}

func TestNewIssuer_ReadsPKCS8AndRefusesWhatIsNotAKey(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(ec)
	issuer, err := NewIssuer(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), "svc")
	if err != nil || issuer.method != jwt.SigningMethodES256 {
		t.Fatalf("an EC key signs ES256: %v", err)
	}
	for name, raw := range map[string][]byte{"empty": nil, "not pem": []byte("key"), "certificate": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}})} {
		if _, err := NewIssuer(raw, "svc"); err == nil {
			t.Errorf("%s: read as a signing key", name)
		}
	}
	raw, _ := rsaKeyPEM(t)
	if _, err := NewIssuer(raw, ""); err == nil {
		t.Error("an issuer with no service")
	}
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, err := NewIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)}), "svc"); err == nil {
		t.Error("a 1024-bit RSA key")
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der384, _ := x509.MarshalPKCS8PrivateKey(p384)
	if _, err := NewIssuer(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der384}), "svc"); err == nil {
		t.Error("a P-384 key zot is not configured for")
	}
}

func TestIssuer_AnEmptyGrantIsAnEmptyAccessList(t *testing.T) {
	issuer, _ := testIssuer(t)
	token, err := issuer.Mint("proj-a", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if !strings.Contains(string(payload), `"access":[]`) {
		t.Errorf("access must be an array zot can read: %s", payload)
	}
}

func expectAll(t *testing.T, checks map[string]bool) {
	t.Helper()
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s: no", name)
		}
	}
}
