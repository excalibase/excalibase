package objectcreds

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testAccount  = "ad5d71d4bbd5e5dd487fb471d72fb788"
	testEndpoint = "https://" + testAccount + ".r2.cloudflarestorage.com"
)

func testParent() Parent {
	return Parent{AccessKeyID: "parent-key", SecretAccessKey: "parent-secret", Endpoint: testEndpoint, Bucket: "backups"}
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func decodeSessionJWT(t *testing.T, sessionToken string) (string, map[string]interface{}) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(sessionToken)
	if err != nil {
		t.Fatalf("session token is not base64: %v", err)
	}
	token, ok := strings.CutPrefix(string(raw), "jwt/")
	if !ok {
		t.Fatalf("session token does not carry the jwt/ marker: %q", raw)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	claims := map[string]interface{}{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return token, claims
}

func TestR2SignerMintsCredentialsScopedToOneBucketAndPrefix(t *testing.T) {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	signer := R2Signer{Now: fixedClock(now)}

	creds, err := signer.Mint(context.Background(), testParent(), Scope{Prefix: "proj1/", Access: ReadWrite, TTL: 12 * time.Hour})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if creds.AccessKeyID != "parent-key" {
		t.Errorf("access key id = %q, want the parent's id (R2 reuses it)", creds.AccessKeyID)
	}
	if creds.SecretAccessKey == "parent-secret" || creds.SessionToken == "" {
		t.Fatalf("minted credentials must never be the parent secret and must carry a session token")
	}
	if !creds.ExpiresAt.Equal(now.Add(12 * time.Hour)) {
		t.Errorf("expires at %v, want %v", creds.ExpiresAt, now.Add(12*time.Hour))
	}

	token, claims := decodeSessionJWT(t, creds.SessionToken)
	digest := sha256.Sum256([]byte(token))
	if creds.SecretAccessKey != hex.EncodeToString(digest[:]) {
		t.Errorf("secret access key must be the SHA-256 hex of the signed JWT")
	}
	parts := strings.Split(token, ".")
	mac := hmac.New(sha256.New, []byte("parent-secret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Errorf("JWT must be HS256-signed with the parent secret")
	}
	want := map[string]interface{}{
		"bucket": "backups", "scope": "object-read-write",
		"sub": testAccount, "iss": "parent-key", "aud": testAccount + ".r2.cloudflarestorage.com",
		"iat": float64(now.Unix()), "exp": float64(now.Add(12 * time.Hour).Unix()),
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("claim %s = %v, want %v", k, claims[k], v)
		}
	}
	paths, _ := claims["paths"].(map[string]interface{})
	prefixes, _ := paths["prefixPaths"].([]interface{})
	if len(prefixes) != 1 || prefixes[0] != "proj1/" {
		t.Errorf("prefixPaths = %v, want exactly [proj1/]", paths["prefixPaths"])
	}
}

func TestR2SignerReadOnlyScope(t *testing.T) {
	creds, err := R2Signer{}.Mint(context.Background(), testParent(), Scope{Prefix: "src/", Access: ReadOnly, TTL: time.Hour})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, claims := decodeSessionJWT(t, creds.SessionToken)
	if claims["scope"] != "object-read-only" {
		t.Errorf("scope = %v, want object-read-only", claims["scope"])
	}
}

func TestR2SignerRefusesWhatCouldWidenOrBreakTheScope(t *testing.T) {
	cases := map[string]struct {
		parent Parent
		scope  Scope
	}{
		"no prefix (whole bucket)":      {testParent(), Scope{Prefix: "", Access: ReadWrite, TTL: time.Hour}},
		"prefix without trailing /":     {testParent(), Scope{Prefix: "proj1", Access: ReadWrite, TTL: time.Hour}},
		"prefix starting with /":        {testParent(), Scope{Prefix: "/proj1/", Access: ReadWrite, TTL: time.Hour}},
		"unknown access":                {testParent(), Scope{Prefix: "p/", Access: "admin-read-write", TTL: time.Hour}},
		"zero ttl":                      {testParent(), Scope{Prefix: "p/", Access: ReadWrite}},
		"ttl beyond seven days":         {testParent(), Scope{Prefix: "p/", Access: ReadWrite, TTL: 8 * 24 * time.Hour}},
		"no bucket":                     {Parent{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testEndpoint}, Scope{Prefix: "p/", Access: ReadWrite, TTL: time.Hour}},
		"no parent secret":              {Parent{AccessKeyID: "k", Endpoint: testEndpoint, Bucket: "b"}, Scope{Prefix: "p/", Access: ReadWrite, TTL: time.Hour}},
		"endpoint is not an R2 account": {Parent{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "http://minio:9000", Bucket: "b"}, Scope{Prefix: "p/", Access: ReadWrite, TTL: time.Hour}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := (R2Signer{}).Mint(context.Background(), tc.parent, tc.scope); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestNewMinterKnowsOnlyTheConfiguredProviders(t *testing.T) {
	for _, name := range []string{ProviderR2, ProviderSTS} {
		if m, err := NewMinter(name); err != nil || m == nil {
			t.Errorf("NewMinter(%q) = %v, %v", name, m, err)
		}
	}
	for _, name := range []string{"", "static", "R2 "} {
		if _, err := NewMinter(name); !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("NewMinter(%q) err = %v, want ErrUnknownProvider", name, err)
		}
	}
}
