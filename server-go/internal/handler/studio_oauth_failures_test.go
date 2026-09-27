package handler

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

var errIdentityStore = errors.New("identity store down")

func TestOAuthStoreFailuresRefuseTheSignIn(t *testing.T) {
	cases := map[string]func(t *testing.T, h *oauthHarness){
		"identity lookup":  func(_ *testing.T, h *oauthHarness) { h.identities.findErr = errIdentityStore },
		"email lookup":     func(_ *testing.T, h *oauthHarness) { h.identities.emailErr = errIdentityStore },
		"user list":        func(_ *testing.T, h *oauthHarness) { h.users.failList = true },
		"account creation": func(_ *testing.T, h *oauthHarness) { h.users.failSave = true },
		"session":          func(_ *testing.T, h *oauthHarness) { h.tokens.failSave = true },
		"link to an existing account": func(t *testing.T, h *oauthHarness) {
			seedPasswordAccount(t, h, true)
			h.identities.linkErr = errIdentityStore
		},
		"verify the linked account": func(t *testing.T, h *oauthHarness) {
			seedPasswordAccount(t, h, false)
			delete(h.links.users, "u-dev")
		},
	}
	for name, breakStore := range cases {
		h := newOAuthHarness(t, false)
		breakStore(t, h)

		w := h.callback("st4te")

		if got := redirectedTo(t, w).Query().Get("oauth_error"); got != "failed" {
			t.Errorf("%s: oauth_error=%q, want failed", name, got)
		}
		if sessionCookie(w) != nil {
			t.Errorf("%s: a session was issued", name)
		}
	}
}

// A new account whose identity cannot be linked would be unreachable, so it
// is removed rather than left behind.
func TestOAuthNewAccountIsRemovedWhenItsIdentityCannotBeLinked(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.identities.linkErr = errIdentityStore
	before := len(h.users.users)

	if got := redirectedTo(t, h.callback("st4te")).Query().Get("oauth_error"); got != "failed" {
		t.Fatalf("oauth_error=%q", got)
	}
	if len(h.users.users) != before {
		t.Fatalf("an unlinked account was left behind: %d accounts, want %d", len(h.users.users), before)
	}
}

func TestOAuthNewAccountIsRemovedWhenItsInviteCannotBeSpent(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.orgs.tokenHash, h.orgs.email = hashToken("tok"), "dev@example.com"
	h.orgs.acceptErr = storage.ErrInviteInvalid
	h.signIn.inviteHash = hashToken("tok")
	before := len(h.users.users)

	w := h.callback("st4te")

	if got := redirectedTo(t, w).Query().Get("oauth_error"); got != "invite_invalid" || sessionCookie(w) != nil {
		t.Fatalf("oauth_error=%q", got)
	}
	if len(h.users.users) != before {
		t.Fatal("the account created for a refused invite was kept")
	}
}

func TestOAuthLinkedAccountThatWasDeactivatedCannotSignIn(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.identities.links["github/4242"] = "u0"
	activateSeededUser(h, "dev@example.com")
	h.users.users["u0"].Active = false

	w := h.callback("st4te")

	if got := redirectedTo(t, w).Query().Get("oauth_error"); got != "account_conflict" || sessionCookie(w) != nil {
		t.Fatalf("oauth_error=%q", got)
	}
}

func TestOAuthExistingAccountInviteRefusalsAreReportedButSignIn(t *testing.T) {
	cases := map[string]struct {
		acceptErr error
		want      string
	}{
		"already a member": {storage.ErrAlreadyOrgMember, "already_member"},
		"invite spent":     {storage.ErrInviteInvalid, "invite_invalid"},
	}
	for name, tc := range cases {
		h := newOAuthHarness(t, false)
		h.identities.links["github/4242"] = "u0"
		activateSeededUser(h, "dev@example.com")
		h.orgs.tokenHash, h.orgs.email, h.orgs.acceptErr = hashToken("tok"), "dev@example.com", tc.acceptErr
		h.signIn.inviteHash = hashToken("tok")

		w := h.callback("st4te")

		loc := redirectedTo(t, w)
		if loc.Path != "/oauth/complete" || loc.Query().Get("invite_error") != tc.want || sessionCookie(w) == nil {
			t.Errorf("%s: redirected to %s", name, loc)
		}
	}
}

// withoutOrgStore rewires the harness for a platform that has no org store,
// where no invite can be honoured.
func (h *oauthHarness) withoutOrgStore() {
	authHandler := NewAuthHandler(h.users, h.tokens)
	authHandler.SetEmailVerifier(NewEmailVerifier(h.links, &recordingSender{}, testStudioURL, ""))
	oauth := NewStudioOAuthHandler(h.signIn, authHandler, h.identities, testStudioURL)
	r := chi.NewRouter()
	r.Route("/api/auth/oauth", func(r chi.Router) { oauth.Routes(r) })
	h.router = r
}

func TestOAuthInvitesAreRefusedWithoutAnOrgStore(t *testing.T) {
	newcomer := newOAuthHarness(t, false)
	newcomer.withoutOrgStore()
	newcomer.signIn.inviteHash = hashToken("tok")
	if got := redirectedTo(t, newcomer.callback("st4te")).Query().Get("oauth_error"); got != "invite_invalid" {
		t.Errorf("new account: oauth_error=%q", got)
	}

	existing := newOAuthHarness(t, false)
	existing.withoutOrgStore()
	existing.identities.links["github/4242"] = "u0"
	activateSeededUser(existing, "dev@example.com")
	existing.signIn.inviteHash = hashToken("tok")
	w := existing.callback("st4te")
	if loc := redirectedTo(t, w); loc.Query().Get("invite_error") != "invite_invalid" || sessionCookie(w) == nil {
		t.Errorf("existing account: redirected to %s", loc)
	}
}

func TestOAuthNewAccountUsernameComesFromTheAddress(t *testing.T) {
	cases := map[string]func(string) bool{
		"Dev.Ops+ci@Example.com": func(name string) bool { return name == "dev-ops-ci" },
		"+++@example.com":        func(name string) bool { return name == "user" },
		"existing@example.org":   func(name string) bool { return strings.HasPrefix(name, "existing-") && len(name) == len("existing-")+6 },
	}
	for address, wantName := range cases {
		h := newOAuthHarness(t, false)
		h.signIn.identity.Email = address

		redirectedTo(t, h.callback("st4te"))

		user, _ := h.identities.FindUserByIdentity(t.Context(), "github", "4242")
		if user == nil || !wantName(user.Username) || user.Email != address {
			t.Errorf("%s: account %+v", address, user)
		}
	}
}

func TestSignInRefusalReadsAsItsReason(t *testing.T) {
	var err error = signInRefusal("invite_only")
	if err.Error() != "invite_only" || accountRefusal(err) != "invite_only" {
		t.Fatalf("refusal %q", err)
	}
	if accountRefusal(errIdentityStore) != "failed" {
		t.Fatal("a store failure was reported as a refusal")
	}
}
