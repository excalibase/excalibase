package adminaccess

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testAud = "aud-tag-123"

type accessFixture struct {
	key      *rsa.PrivateKey
	kid      string
	server   *httptest.Server
	fetches  atomic.Int32
	team     string
	verifier *Verifier
}

func jwksBody(kid string, key *rsa.PublicKey) []byte {
	body, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	return body
}

func newFixture(t *testing.T) *accessFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{key: key, kid: "kid-1"}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksBody(f.kid, &f.key.PublicKey))
	}))
	t.Cleanup(f.server.Close)
	f.team = f.server.Listener.Addr().String()
	f.verifier = NewVerifier(Config{TeamDomain: f.team, Audience: testAud, HTTPClient: f.server.Client()})
	return f
}

func (f *accessFixture) token(t *testing.T, mutate func(claims jwt.MapClaims, header map[string]any)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"aud": []string{testAud}, "iss": "https://" + f.team,
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(), "email": "ops@example.test",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	if mutate != nil {
		mutate(claims, token.Header)
	}
	signed, err := token.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestVerifyAcceptsAValidAssertion(t *testing.T) {
	f := newFixture(t)
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatalf("a valid assertion was refused: %v", err)
	}
}

func TestVerifyRefusesWhatIsNotValid(t *testing.T) {
	f := newFixture(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]string{
		"empty":          "",
		"garbage":        "not-a-jwt",
		"wrong audience": f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"another-app"} }),
		"wrong issuer":   f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://evil.example.test" }),
		"expired":        f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"not yet valid":  f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }),
		"no expiry":      f.token(t, func(c jwt.MapClaims, _ map[string]any) { delete(c, "exp") }),
		"unknown key id": f.token(t, func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "kid-unknown" }),
	}
	forged := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"aud": []string{testAud}, "iss": "https://" + f.team, "exp": time.Now().Add(time.Hour).Unix()})
	forged.Header["kid"] = f.kid
	cases["signed by another key"], _ = forged.SignedString(other)
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"aud": []string{testAud}, "iss": "https://" + f.team, "exp": time.Now().Add(time.Hour).Unix()})
	cases["alg none"], _ = none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	hmac := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"aud": []string{testAud}, "iss": "https://" + f.team, "exp": time.Now().Add(time.Hour).Unix()})
	hmac.Header["kid"] = f.kid
	cases["HS256 with the public key as secret"], _ = hmac.SignedString(f.key.PublicKey.N.Bytes())

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if err := f.verifier.Verify(token); err == nil {
				t.Fatal("the assertion was accepted")
			}
		})
	}
}

func TestVerifyCachesTheKeysAndRefetchesOnAnUnknownKid(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		if err := f.verifier.Verify(f.token(t, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("fetched the keys %d times for three requests, want 1", got)
	}

	// Cloudflare rotated its key: a new kid is looked for once, then accepted.
	f.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	f.kid = "kid-2"
	f.verifier.minRefetch = 0
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatalf("a rotated key was not picked up: %v", err)
	}
	if got := f.fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}
}

func TestVerifyDoesNotFetchOnEveryBadKid(t *testing.T) {
	f := newFixture(t)
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	bad := f.token(t, func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "kid-unknown" })
	for i := 0; i < 5; i++ {
		_ = f.verifier.Verify(bad)
	}
	if got := f.fetches.Load(); got > 2 {
		t.Fatalf("a flood of unknown key ids made %d fetches", got)
	}
}

func TestVerifyFailsClosedWhenTheKeysCannotBeRead(t *testing.T) {
	f := newFixture(t)
	token := f.token(t, nil)
	f.server.Close()
	if err := f.verifier.Verify(token); err == nil {
		t.Fatal("an assertion was accepted without the keys that check it")
	}
}
