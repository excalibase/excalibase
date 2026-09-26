package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
)

type fakeSignIn struct {
	identity   studiooauth.Identity
	inviteHash string
	finishErr  error
	started    []string
}

func (f *fakeSignIn) Configured() []string { return []string{"google", "github"} }

func (f *fakeSignIn) Start(_ context.Context, provider, inviteHash string) (string, string, error) {
	if provider == "gitlab" {
		return "", "", studiooauth.ErrUnknownProvider
	}
	f.started = append(f.started, provider+":"+inviteHash)
	return "https://provider.example/authorize?state=st4te", "st4te", nil
}

func (f *fakeSignIn) Finish(_ context.Context, _, queryState, browserState, _ string) (studiooauth.Identity, string, error) {
	if queryState != browserState {
		return studiooauth.Identity{}, "", studiooauth.ErrStateInvalid
	}
	return f.identity, f.inviteHash, f.finishErr
}

type memIdentities struct {
	users *mockUserStore
	links map[string]string
}

func (m *memIdentities) FindUserByIdentity(_ context.Context, provider, subject string) (*domain.User, error) {
	if id, ok := m.links[provider+"/"+subject]; ok {
		return m.users.users[id], nil
	}
	return nil, nil
}

func (m *memIdentities) FindUserByEmailFold(_ context.Context, address string) (*domain.User, error) {
	for _, u := range m.users.users {
		if strings.EqualFold(u.Email, address) {
			return u, nil
		}
	}
	return nil, nil
}

func (m *memIdentities) LinkStudioIdentity(_ context.Context, provider, subject, userID, _ string) error {
	m.links[provider+"/"+subject] = userID
	return nil
}

type oauthHarness struct {
	router     chi.Router
	signIn     *fakeSignIn
	users      *mockUserStore
	tokens     *mockTokenStore
	identities *memIdentities
	orgs       *inviteOrgStore
	links      *fakeVerificationStore
}

func newOAuthHarness(t *testing.T, inviteOnly bool) *oauthHarness {
	t.Helper()
	h := &oauthHarness{
		signIn: &fakeSignIn{identity: studiooauth.Identity{Provider: "github", Subject: "4242", Email: "dev@example.com"}},
		users:  seededStore(), tokens: newMockTokenStore(), orgs: &inviteOrgStore{},
		links: newFakeVerificationStore(),
	}
	h.identities = &memIdentities{users: h.users, links: map[string]string{}}
	authHandler := NewAuthHandler(h.users, h.tokens)
	authHandler.SetOrgStore(h.orgs)
	authHandler.SetInviteOnly(inviteOnly)
	authHandler.SetEmailVerifier(NewEmailVerifier(h.links, &recordingSender{}, testStudioURL, ""))
	oauth := NewStudioOAuthHandler(h.signIn, authHandler, h.identities, testStudioURL)
	r := chi.NewRouter()
	r.Route("/api/auth/oauth", func(r chi.Router) { oauth.Routes(r) })
	h.router = r
	return h
}

func (h *oauthHarness) callback(browserState string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/auth/oauth/github/callback?code=c&state=st4te", nil)
	if browserState != "" {
		req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: browserState})
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func redirectedTo(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("got %d, want a redirect: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	return u
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func activateSeededUser(h *oauthHarness, address string) {
	now := time.Now()
	seeded := h.users.users["u0"]
	seeded.Email, seeded.Active, seeded.EmailVerifiedAt = address, true, &now
}

func TestOAuthProvidersListsTheConfiguredOnes(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := doRequest(h.router, "GET", "/api/auth/oauth/providers", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"github"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// Start binds the sign-in to this browser with a cookie the provider's
// cross-site redirect back will still carry (SameSite=Lax, never Strict).
func TestOAuthStartSendsTheBrowserToTheProviderWithABoundState(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := doRequest(h.router, "GET", "/api/auth/oauth/github/start?invite=tok", "")
	if loc := redirectedTo(t, w); loc.Host != "provider.example" {
		t.Fatalf("redirected to %s", loc)
	}
	var state *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthStateCookie {
			state = c
		}
	}
	if state == nil || state.Value != "st4te" || !state.HttpOnly || !state.Secure || state.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie: %+v", state)
	}
	if h.signIn.started[0] != "github:"+hashToken("tok") {
		t.Fatalf("started %v: the invite must travel as its hash", h.signIn.started)
	}
	if w := doRequest(h.router, "GET", "/api/auth/oauth/gitlab/start", ""); redirectedTo(t, w).Query().Get("oauth_error") != "unavailable" {
		t.Fatal("an unknown provider was not refused")
	}
}

func TestOAuthNewPersonGetsAVerifiedAccountAndASession(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := h.callback("st4te")

	if loc := redirectedTo(t, w); loc.Path != "/oauth/complete" {
		t.Fatalf("redirected to %s", loc)
	}
	if sessionCookie(w) == nil {
		t.Fatal("no session was issued")
	}
	user, _ := h.identities.FindUserByIdentity(context.Background(), "github", "4242")
	if user == nil || user.Email != "dev@example.com" || user.EmailVerifiedAt == nil || user.Role != "user" || user.IsService() {
		t.Fatalf("created account: %+v", user)
	}
}

// An account that already uses the address is linked, not duplicated, and
// the provider's verification verifies it.
func TestOAuthLinksAnExistingAccountWithTheSameEmail(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.users.users["u-dev"] = &domain.User{ID: "u-dev", Username: "dev", Email: "Dev@Example.com", Active: true, Kind: domain.UserKindHuman}
	h.links.users["u-dev"] = "Dev@Example.com"
	before := len(h.users.users)

	redirectedTo(t, h.callback("st4te"))

	if len(h.users.users) != before {
		t.Fatal("a second account was created for the same address")
	}
	if h.identities.links["github/4242"] != "u-dev" {
		t.Fatalf("links: %v", h.identities.links)
	}
	if _, ok := h.links.verified["u-dev"]; !ok {
		t.Fatal("the provider-verified address did not verify the account")
	}
	redirectedTo(t, h.callback("st4te"))
	if len(h.users.users) != before {
		t.Fatal("signing in again created an account")
	}
}

func TestOAuthNeverSignsInAsAServicePrincipal(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.users.users["svc"] = &domain.User{ID: "svc", Username: "svc-auth", Email: "dev@example.com", Kind: domain.UserKindService}
	if loc := redirectedTo(t, h.callback("st4te")); loc.Query().Get("oauth_error") != "account_conflict" {
		t.Fatalf("redirected to %s", loc)
	}
}

func TestOAuthRefusalsIssueNoSession(t *testing.T) {
	cases := map[string]struct {
		browserState string
		finishErr    error
		want         string
	}{
		"state from another browser": {"other", nil, "state"},
		"no state cookie":            {"", nil, "state"},
		"unverified provider email":  {"st4te", studiooauth.ErrEmailNotVerified, "email_not_verified"},
		"exchange failed":            {"st4te", errors.New("invalid_grant"), "failed"},
	}
	for name, tc := range cases {
		h := newOAuthHarness(t, false)
		h.signIn.finishErr = tc.finishErr
		w := h.callback(tc.browserState)
		if got := redirectedTo(t, w).Query().Get("oauth_error"); got != tc.want {
			t.Errorf("%s: oauth_error=%q, want %q", name, got, tc.want)
		}
		if sessionCookie(w) != nil || len(h.tokens.tokens) != 0 {
			t.Errorf("%s: a session was issued", name)
		}
	}
}

func TestOAuthProviderDenialIsReported(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := doRequest(h.router, "GET", "/api/auth/oauth/github/callback?error=access_denied&state=st4te", "")
	if got := redirectedTo(t, w).Query().Get("oauth_error"); got != "cancelled" {
		t.Fatalf("oauth_error=%q", got)
	}
}

// Sign-up rules match password sign-up: the first admin comes from the setup
// token, an invite-only platform needs an invite, and an invite only works
// for its own address.
func TestOAuthSignUpFollowsTheRegistrationRules(t *testing.T) {
	empty := newOAuthHarness(t, false)
	delete(empty.users.users, "u0")
	if got := redirectedTo(t, empty.callback("st4te")).Query().Get("oauth_error"); got != "setup_required" {
		t.Errorf("first account: %q", got)
	}

	closed := newOAuthHarness(t, true)
	if got := redirectedTo(t, closed.callback("st4te")).Query().Get("oauth_error"); got != "invite_only" {
		t.Errorf("invite-only without an invite: %q", got)
	}

	mismatched := newOAuthHarness(t, true)
	mismatched.orgs.tokenHash = hashToken("tok")
	mismatched.signIn.inviteHash = hashToken("tok")
	if got := redirectedTo(t, mismatched.callback("st4te")).Query().Get("oauth_error"); got != "invite_invalid" {
		t.Errorf("invite for another address: %q", got)
	}
	if len(mismatched.orgs.accepted) != 0 {
		t.Error("a mismatched invite was spent")
	}
}

func TestOAuthSignUpWithAnInviteJoinsItsOrg(t *testing.T) {
	h := newOAuthHarness(t, true)
	h.orgs.tokenHash, h.orgs.email = hashToken("tok"), "dev@example.com"
	h.signIn.inviteHash = hashToken("tok")

	loc := redirectedTo(t, h.callback("st4te"))
	if loc.Path != "/oauth/complete" || loc.Query().Get("joined") != "1" {
		t.Fatalf("redirected to %s", loc)
	}
	if len(h.orgs.accepted) != 1 {
		t.Fatal("the invite was not spent")
	}
}

func TestOAuthCallbackClearsTheStateCookie(t *testing.T) {
	h := newOAuthHarness(t, false)
	w := h.callback("st4te")
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthStateCookie && c.MaxAge >= 0 {
			t.Fatalf("state cookie left in place: %+v", c)
		}
	}
}

// An existing developer who follows an invite link and signs in with a
// provider joins the org, when the invite names their address.
func TestOAuthExistingAccountAcceptsAnInviteForItsAddress(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.identities.links["github/4242"] = "u0"
	activateSeededUser(h, "dev@example.com")
	h.orgs.tokenHash, h.orgs.email = hashToken("tok"), "dev@example.com"
	h.signIn.inviteHash = hashToken("tok")

	loc := redirectedTo(t, h.callback("st4te"))
	if loc.Path != "/oauth/complete" || loc.Query().Get("joined") != "1" || len(h.orgs.accepted) != 1 {
		t.Fatalf("redirected to %s, accepted %v", loc, h.orgs.accepted)
	}
}

func TestOAuthExistingAccountSignsInEvenWhenTheInviteIsNotTheirs(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.identities.links["github/4242"] = "u0"
	activateSeededUser(h, "dev@example.com")
	h.orgs.tokenHash = hashToken("tok")
	h.signIn.inviteHash = hashToken("tok")

	w := h.callback("st4te")
	loc := redirectedTo(t, w)
	if loc.Path != "/oauth/complete" || loc.Query().Get("invite_error") != "invite_invalid" || sessionCookie(w) == nil {
		t.Fatalf("redirected to %s", loc)
	}
	if len(h.orgs.accepted) != 0 {
		t.Fatal("an invite for another address was spent")
	}
}
