package studiooauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type mapSecrets map[string]map[string]string

func (m mapSecrets) Get(path string) (map[string]string, error) {
	if data, ok := m[path]; ok {
		return data, nil
	}
	return nil, errors.New("not found")
}

type memStates struct {
	mu      sync.Mutex
	records map[string]memState
}

type memState struct {
	state   domain.OAuthState
	expires time.Time
}

func (m *memStates) SaveOAuthState(_ context.Context, hash string, state domain.OAuthState, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[hash] = memState{state: state, expires: expires}
	return nil
}

func (m *memStates) ConsumeOAuthState(_ context.Context, hash string, now time.Time) (*domain.OAuthState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[hash]
	delete(m.records, hash)
	if !ok || !now.Before(rec.expires) {
		return nil, ErrStateInvalid
	}
	return &rec.state, nil
}

// fakeProvider is an OAuth server that holds the client to PKCE and to its
// secret, then reports whatever identity the test sets.
type fakeProvider struct {
	t          *testing.T
	server     *httptest.Server
	challenge  string
	googleUser map[string]any
	githubMail []map[string]any
	failToken  bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{t: t, googleUser: map[string]any{"sub": "g-1", "email": "dev@example.com", "email_verified": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.requireBearer(r)
		_ = json.NewEncoder(w).Encode(f.googleUser)
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		f.requireBearer(r)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242, "login": "dev"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		f.requireBearer(r)
		_ = json.NewEncoder(w).Encode(f.githubMail)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeProvider) requireBearer(r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer provider-access-token" {
		f.t.Errorf("identity call without the access token: %q", r.Header.Get("Authorization"))
	}
}

func (f *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	pkceOK := base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge
	clientID, secret, basic := r.BasicAuth()
	if !basic {
		clientID, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if f.failToken || !pkceOK || r.Form.Get("code") != "the-code" || clientID != "cid" || secret != "csecret" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "provider-access-token", "token_type": "bearer"})
}

func (f *fakeProvider) endpoints() Endpoints {
	return Endpoints{AuthURL: f.server.URL + "/authorize", TokenURL: f.server.URL + "/token", APIBase: f.server.URL}
}

const callbackBase = "https://studio.example.com"

func newService(t *testing.T, fake *fakeProvider, secrets mapSecrets) (*Service, *memStates) {
	t.Helper()
	states := &memStates{records: map[string]memState{}}
	svc := New([]Provider{Google(fake.endpoints()), GitHub(fake.endpoints())}, secrets, states, callbackBase, fake.server.Client())
	return svc, states
}

func configured() mapSecrets {
	return mapSecrets{
		"oauth/studio/google": {"client_id": "cid", "client_secret": "csecret"},
		"oauth/studio/github": {"client_id": "cid", "client_secret": "csecret"},
	}
}

// start runs Start and records the challenge the fake provider must see.
func start(t *testing.T, svc *Service, fake *fakeProvider, provider, inviteHash string) string {
	t.Helper()
	redirect, state, err := svc.Start(context.Background(), provider, inviteHash)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	if q.Get("state") != state || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" {
		t.Fatalf("authorize URL %s", redirect)
	}
	if q.Get("redirect_uri") != callbackBase+"/api/auth/oauth/"+provider+"/callback" {
		t.Fatalf("redirect_uri = %s", q.Get("redirect_uri"))
	}
	if q.Get("client_secret") != "" {
		t.Fatal("the client secret reached the browser")
	}
	fake.challenge = q.Get("code_challenge")
	return state
}

func TestGoogleSignInReturnsTheVerifiedIdentity(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, configured())
	state := start(t, svc, fake, "google", "invite-hash")

	identity, inviteHash, err := svc.Finish(context.Background(), "google", state, state, "the-code")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if identity != (Identity{Provider: "google", Subject: "g-1", Email: "dev@example.com"}) || inviteHash != "invite-hash" {
		t.Fatalf("identity %+v invite %q", identity, inviteHash)
	}
}

func TestGitHubSignInUsesThePrimaryVerifiedEmail(t *testing.T) {
	fake := newFakeProvider(t)
	fake.githubMail = []map[string]any{
		{"email": "old@example.com", "primary": false, "verified": true},
		{"email": "dev@example.com", "primary": true, "verified": true},
	}
	svc, _ := newService(t, fake, configured())
	state := start(t, svc, fake, "github", "")

	identity, _, err := svc.Finish(context.Background(), "github", state, state, "the-code")
	if err != nil || identity != (Identity{Provider: "github", Subject: "4242", Email: "dev@example.com"}) {
		t.Fatalf("identity %+v err %v", identity, err)
	}
}

func TestAnUnverifiedProviderEmailIsRefused(t *testing.T) {
	fake := newFakeProvider(t)
	fake.googleUser["email_verified"] = false
	fake.githubMail = []map[string]any{{"email": "dev@example.com", "primary": true, "verified": false}}
	svc, _ := newService(t, fake, configured())
	for _, provider := range []string{"google", "github"} {
		state := start(t, svc, fake, provider, "")
		if _, _, err := svc.Finish(context.Background(), provider, state, state, "the-code"); !errors.Is(err, ErrEmailNotVerified) {
			t.Errorf("%s: got %v, want ErrEmailNotVerified", provider, err)
		}
	}
}

// The state proves the callback belongs to the browser that started it, and
// it is spent on first use.
func TestTheCallbackStateMustMatchAndIsSpentOnce(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, configured())
	state := start(t, svc, fake, "google", "")

	if _, _, err := svc.Finish(context.Background(), "google", state, "another-browser", "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("mismatched cookie: %v", err)
	}
	state = start(t, svc, fake, "google", "")
	if _, _, err := svc.Finish(context.Background(), "github", state, state, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("state for another provider: %v", err)
	}
	state = start(t, svc, fake, "google", "")
	if _, _, err := svc.Finish(context.Background(), "google", state, state, "the-code"); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, _, err := svc.Finish(context.Background(), "google", state, state, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("replayed state: %v", err)
	}
}

func TestAnExpiredStateIsRefused(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, configured())
	issued := time.Now()
	svc.now = func() time.Time { return issued }
	state := start(t, svc, fake, "google", "")
	svc.now = func() time.Time { return issued.Add(stateTTL + time.Second) }
	if _, _, err := svc.Finish(context.Background(), "google", state, state, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("got %v", err)
	}
}

// The provider refuses a code exchanged without the matching verifier; a
// tampered challenge therefore fails the sign-in.
func TestAWrongVerifierFailsTheExchange(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, configured())
	state := start(t, svc, fake, "google", "")
	fake.challenge = "tampered"
	if _, _, err := svc.Finish(context.Background(), "google", state, state, "the-code"); err == nil {
		t.Fatal("the exchange succeeded without PKCE")
	}
}

func TestOnlyConfiguredProvidersAreOffered(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, mapSecrets{"oauth/studio/github": {"client_id": "cid", "client_secret": "csecret"}})
	if got := svc.Configured(); !slices.Equal(got, []string{"github"}) {
		t.Fatalf("configured = %v", got)
	}
	if _, _, err := svc.Start(context.Background(), "google", ""); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("unconfigured start: %v", err)
	}
	if _, _, err := svc.Start(context.Background(), "gitlab", ""); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown start: %v", err)
	}
	noVault := New([]Provider{Google(fake.endpoints())}, nil, &memStates{records: map[string]memState{}}, callbackBase, fake.server.Client())
	if len(noVault.Configured()) != 0 {
		t.Fatal("a service without a vault offers providers")
	}
	var nilService *Service
	if len(nilService.Configured()) != 0 {
		t.Fatal("a nil service offers providers")
	}
}
