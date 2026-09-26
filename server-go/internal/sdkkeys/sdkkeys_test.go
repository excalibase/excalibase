package sdkkeys

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type mapVault map[string]map[string]string

func (v mapVault) Get(path string) (map[string]string, error) {
	data, ok := v[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

func newSigningKey(t *testing.T) (*ecdsa.PrivateKey, mapVault) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	return key, mapVault{signingKeyPath: {"key": keyPEM}}
}

func parse(t *testing.T, key *ecdsa.PrivateKey, raw string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return claims
}

// The token must be one only auth's key routes accept: the auth-only
// audience, the key_admin use, and a lifetime of at most a minute.
func TestSignerMintsAShortAuthOnlyKeyAdminToken(t *testing.T) {
	key, vault := newSigningKey(t)
	raw, err := NewSigner(vault).Sign("proj-a", "acme")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	claims := parse(t, key, raw)
	if claims["token_use"] != "key_admin" || claims["projectId"] != "proj-a" || claims["iss"] != "excalibase" {
		t.Fatalf("claims: %v", claims)
	}
	aud, _ := claims.GetAudience()
	if !slices.Equal([]string(aud), []string{"excalibase-auth:proj-a"}) {
		t.Fatalf("aud = %v", aud)
	}
	iat, _ := claims.GetIssuedAt()
	exp, _ := claims.GetExpirationTime()
	if lifetime := exp.Sub(iat.Time); lifetime <= 0 || lifetime > time.Minute {
		t.Fatalf("lifetime = %v", lifetime)
	}
	if _, hasScope := claims["scope"]; hasScope {
		t.Fatal("a key-admin token carries no end-user scope")
	}
}

func TestSignerFailsWithoutTheSigningKey(t *testing.T) {
	if _, err := NewSigner(mapVault{}).Sign("proj-a", "acme"); err == nil {
		t.Fatal("signed without a key")
	}
	if _, err := NewSigner(mapVault{signingKeyPath: {"key": "not pem"}}).Sign("proj-a", "acme"); err == nil {
		t.Fatal("signed with a malformed key")
	}
}

// fakeAuth answers like excalibase-auth's api-key routes and records the
// bearer it was given.
type fakeAuth struct {
	key     *ecdsa.PrivateKey
	t       *testing.T
	status  int
	paths   []string
	bearers []string
}

func (f *fakeAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.bearers = append(f.bearers, bearer)
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"error":"keyType must be 'publishable' or 'secret'"}`))
		return
	}
	switch r.Method {
	case http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7,"plaintext":"esk_pub_live_abc","keyPrefix":"abc","keyType":"publishable","name":"web","createdAt":"2026-09-27T00:00:00Z"}`))
	case http.MethodGet:
		_, _ = w.Write([]byte(`{"keys":[{"id":7,"keyPrefix":"abc","keyType":"publishable","name":"web","createdAt":"2026-09-27T00:00:00Z"}]}`))
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	}
}

func newClient(t *testing.T, auth *fakeAuth) *Client {
	t.Helper()
	key, vault := newSigningKey(t)
	auth.key, auth.t = key, t
	server := httptest.NewServer(auth)
	t.Cleanup(server.Close)
	return NewClient(server.URL, NewSigner(vault), server.Client())
}

func TestClientCreatesListsAndRevokesThroughAuth(t *testing.T) {
	auth := &fakeAuth{}
	client := newClient(t, auth)
	ctx := context.Background()

	created, err := client.Create(ctx, "acme", "proj-a", CreateRequest{Name: "web", KeyType: "publishable"})
	if err != nil || created.Plaintext != "esk_pub_live_abc" || created.ID != 7 {
		t.Fatalf("Create: %+v %v", created, err)
	}
	keys, err := client.List(ctx, "acme", "proj-a")
	if err != nil || len(keys) != 1 || keys[0].KeyPrefix != "abc" {
		t.Fatalf("List: %+v %v", keys, err)
	}
	if err := client.Revoke(ctx, "acme", "proj-a", 7); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	want := []string{"POST /auth/acme/proj-a/api-keys/", "GET /auth/acme/proj-a/api-keys/", "DELETE /auth/acme/proj-a/api-keys/7"}
	if !slices.Equal(auth.paths, want) {
		t.Fatalf("paths = %v", auth.paths)
	}
	for _, bearer := range auth.bearers {
		if parse(t, auth.key, bearer)["projectId"] != "proj-a" {
			t.Fatal("a call carried no key-admin token for the project")
		}
	}
	if auth.bearers[0] == auth.bearers[1] && auth.bearers[1] == auth.bearers[2] {
		t.Error("expected a fresh token per call")
	}
}

func TestClientReportsAuthRefusals(t *testing.T) {
	client := newClient(t, &fakeAuth{status: http.StatusBadRequest})
	_, err := client.Create(context.Background(), "acme", "proj-a", CreateRequest{Name: "x", KeyType: "publishable"})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Status != http.StatusBadRequest || !strings.Contains(refused.Message, "keyType") {
		t.Fatalf("got %v", err)
	}

	unavailable := newClient(t, &fakeAuth{status: http.StatusServiceUnavailable})
	if _, err := unavailable.List(context.Background(), "acme", "proj-a"); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("503: got %v", err)
	}
}

func TestClientReportsAnUnreachableAuth(t *testing.T) {
	_, vault := newSigningKey(t)
	client := NewClient("http://127.0.0.1:1", NewSigner(vault), &http.Client{Timeout: time.Second})
	if _, err := client.List(context.Background(), "acme", "proj-a"); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestClientRefusesPathInjection(t *testing.T) {
	client := newClient(t, &fakeAuth{})
	if _, err := client.List(context.Background(), "acme/../x", "proj-a"); err == nil {
		t.Fatal("an unsafe org slug reached auth")
	}
}
