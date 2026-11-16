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

// A slow key fetch must not stall verification that the cached keys can
// already answer: the lock is not held across the network call.
func TestVerifyDoesNotHoldTheLockDuringAKeyFetch(t *testing.T) {
	f := newFixture(t)
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	f.verifier.minRefetch = 0

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Write(jwksBody(f.kid, &f.key.PublicKey))
	}))
	defer slow.Close()
	defer close(release)
	f.verifier.cfg.TeamDomain = slow.Listener.Addr().String()
	f.verifier.cfg.HTTPClient = slow.Client()

	unknown := f.token(t, func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "kid-new" })
	go func() { _ = f.verifier.Verify(unknown) }()
	<-entered

	done := make(chan error, 1)
	go func() {
		// signed for the original issuer; only the cached key lookup matters here
		done <- f.verifier.Verify(f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://" + f.verifier.cfg.TeamDomain }))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cached key was refused during a fetch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("verification of a cached key waited on another request's key fetch")
	}
}

// A keys endpoint that answers badly is an error, never an accepted assertion.
func TestVerifyRefusesWhenTheKeysEndpointAnswersBadly(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"a server error": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"not json":       func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("<html>")) },
		"no keys":        func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"keys":[]}`)) },
		"only unusable entries": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"keys":[{"kty":"EC","kid":"a"},{"kty":"RSA","kid":"b","n":"!!","e":"!!"},{"kty":"RSA","n":"AQAB","e":"AQAB"}]}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			f := newFixture(t)
			verifier := NewVerifier(Config{TeamDomain: server.Listener.Addr().String(), Audience: testAud, HTTPClient: server.Client()})
			token := f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://" + server.Listener.Addr().String() })
			if err := verifier.Verify(token); err == nil {
				t.Fatal("an assertion was accepted although no key could be read")
			}
		})
	}
}

// A refresh that fails after keys were cached keeps serving the cached keys.
func TestVerifyKeepsTheCachedKeysWhenARefreshFails(t *testing.T) {
	f := newFixture(t)
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	f.verifier.minRefetch = 0
	f.verifier.mu.Lock()
	f.verifier.fetchedAt = time.Now().Add(-2 * keyLifetime)
	f.verifier.mu.Unlock()
	f.server.Close()
	if err := f.verifier.Verify(f.token(t, nil)); err != nil {
		t.Fatalf("a stale key was refused when the refresh failed: %v", err)
	}
}

func TestVerifyRefusesAnAssertionWithoutAKeyId(t *testing.T) {
	f := newFixture(t)
	if err := f.verifier.Verify(f.token(t, func(_ jwt.MapClaims, h map[string]any) { delete(h, "kid") })); err == nil {
		t.Fatal("an assertion naming no key was accepted")
	}
}
