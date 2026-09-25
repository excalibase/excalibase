//go:build integration

package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

const inviteeEmail = "carol@invited.test"

type inviteFixture struct {
	orgs     chi.Router
	register chi.Router
	auth     *AuthHandler
	store    *pgstore.Store
	orgID    string
}

func newInviteFixture(t *testing.T, inviteOnly bool) *inviteFixture {
	t.Helper()
	orgs, store := setupOrgRouter(t)
	authHandler := NewAuthHandler(store, store)
	authHandler.SetOrgStore(store)
	authHandler.SetInviteOnly(inviteOnly)
	register := chi.NewRouter()
	register.Post(testRegisterPath, authHandler.Register)

	w := orgRequest(orgs, "POST", testOrgsPath, `{"name":"InviteOrg","slug":"invite-org"}`, testAliceID)
	var org domain.Org
	if err := json.NewDecoder(w.Body).Decode(&org); err != nil || org.ID == "" {
		t.Fatalf("create org: %d %s", w.Code, w.Body.String())
	}
	return &inviteFixture{orgs: orgs, register: register, auth: authHandler, store: store, orgID: org.ID}
}

// invite has alice invite an address with no account and returns the token
// carried by the link the server hands back.
func (f *inviteFixture) invite(t *testing.T, email, role string) string {
	t.Helper()
	w := orgRequest(f.orgs, "POST", testOrgsSlash+f.orgID+testMembersPath,
		fmt.Sprintf(`{"email":%q,"role":%q}`, email, role), testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status     string `json:"status"`
		InviteLink string `json:"inviteLink"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	link, err := url.Parse(resp.InviteLink)
	if err != nil || resp.Status != "pending" || link.Path != "/register" {
		t.Fatalf("invite answered %+v, want a pending invite with a /register link", resp)
	}
	token := link.Query().Get("invite")
	if token == "" {
		t.Fatalf("invite link carries no token: %s", resp.InviteLink)
	}
	return token
}

func (f *inviteFixture) registerAs(username, email, token string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q,"inviteToken":%q}`,
		username, email, testutil.FixturePassword("invite-"+username), token)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	f.register.ServeHTTP(w, req)
	return w
}

func (f *inviteFixture) accept(userID, token string) *httptest.ResponseRecorder {
	return orgRequest(f.orgs, "POST", testOrgsPath+"/invites/accept",
		fmt.Sprintf(`{"token":%q}`, token), userID)
}

func (f *inviteFixture) roleOf(t *testing.T, email string) string {
	t.Helper()
	members, err := f.store.ListOrgMembers(t.Context(), f.orgID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	for _, m := range members {
		if m.Email == email {
			return m.Role
		}
	}
	return ""
}

func (f *inviteFixture) userID(t *testing.T, username string) string {
	t.Helper()
	u, err := f.store.FindUserByUsername(t.Context(), username)
	if err != nil || u == nil {
		t.Fatalf("find %s: %v", username, err)
	}
	return u.ID
}

func (f *inviteFixture) expire(t *testing.T) {
	t.Helper()
	if _, err := f.store.DB().ExecContext(t.Context(),
		`UPDATE pending_invites SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatalf("expire invites: %v", err)
	}
}

func TestInviteLink_RegisteringWithTheInvitedEmailButNoTokenDoesNotJoin(t *testing.T) {
	f := newInviteFixture(t, false)
	f.invite(t, inviteeEmail, domain.OrgRoleAdmin)

	if w := f.registerAs("carol", inviteeEmail, ""); w.Code != http.StatusCreated {
		t.Fatalf("open registration without a token: %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != "" {
		t.Fatalf("an email match alone joined the org as %s", role)
	}
	invites, _ := f.store.ListPendingInvites(t.Context(), f.orgID)
	if len(invites) != 1 {
		t.Errorf("the unclaimed invite must stay pending, got %d", len(invites))
	}
}

func TestInviteLink_RegisteringWithTheTokenJoinsOnce(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleDeveloper)

	if w := f.registerAs("carol", inviteeEmail, token); w.Code != http.StatusCreated {
		t.Fatalf("register with token: %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != domain.OrgRoleDeveloper {
		t.Fatalf("carol's role = %q, want developer", role)
	}

	w := f.registerAs("mallory", "mallory@evil.test", token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a reused token must be refused, got %d %s", w.Code, w.Body.String())
	}
	if u, _ := f.store.FindUserByUsername(t.Context(), "mallory"); u != nil {
		t.Error("a refused invite must not leave an account behind")
	}
	if f.roleOf(t, "mallory@evil.test") != "" {
		t.Error("a reused token joined a second account")
	}
}

func TestInviteLink_ExpiredTokenIsRefused(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleViewer)
	f.expire(t)

	if w := f.registerAs("carol", inviteeEmail, token); w.Code != http.StatusBadRequest {
		t.Fatalf("an expired token must be refused, got %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != "" {
		t.Errorf("an expired token joined the org as %s", role)
	}
}

func TestInviteLink_UnknownTokenIsRefused(t *testing.T) {
	f := newInviteFixture(t, false)
	f.invite(t, inviteeEmail, domain.OrgRoleViewer)

	if w := f.registerAs("carol", inviteeEmail, strings.Repeat("ab", 32)); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown token must be refused, got %d %s", w.Code, w.Body.String())
	}
}

func TestInviteLink_OnlyTheTokenHashIsStored(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleViewer)

	var stored string
	if err := f.store.DB().QueryRowContext(t.Context(),
		`SELECT token_hash FROM pending_invites WHERE email = $1`, inviteeEmail).Scan(&stored); err != nil {
		t.Fatalf("read invite: %v", err)
	}
	sum := sha256.Sum256([]byte(token))
	if stored == token || stored != hex.EncodeToString(sum[:]) {
		t.Errorf("stored %q, want the SHA-256 of the token", stored)
	}
}

func TestInviteLink_InvitingAgainReplacesTheLink(t *testing.T) {
	f := newInviteFixture(t, false)
	first := f.invite(t, inviteeEmail, domain.OrgRoleViewer)
	second := f.invite(t, inviteeEmail, domain.OrgRoleDeveloper)

	if w := f.registerAs("carol", inviteeEmail, first); w.Code != http.StatusBadRequest {
		t.Fatalf("the replaced link must be refused, got %d", w.Code)
	}
	if w := f.registerAs("carol", inviteeEmail, second); w.Code != http.StatusCreated {
		t.Fatalf("the new link must work, got %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != domain.OrgRoleDeveloper {
		t.Errorf("role = %q, want the re-invite's developer", role)
	}
}

func TestInviteLink_SignedInUserAcceptsOnce(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleDeveloper)
	if w := f.registerAs("carol", inviteeEmail, ""); w.Code != http.StatusCreated {
		t.Fatalf("register: %d", w.Code)
	}
	carol := f.userID(t, "carol")

	if w := f.accept(carol, token); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != domain.OrgRoleDeveloper {
		t.Fatalf("role after accept = %q", role)
	}
	if w := f.accept(testBobID, token); w.Code != http.StatusBadRequest {
		t.Fatalf("a reused token must be refused on accept, got %d", w.Code)
	}
	if f.roleOf(t, "bob@test.com") != "" {
		t.Error("bob joined with a spent token")
	}
}

func TestInviteLink_AcceptRefusesAnExpiredToken(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleDeveloper)
	f.expire(t)

	if w := f.accept(testBobID, token); w.Code != http.StatusBadRequest {
		t.Fatalf("an expired token must be refused on accept, got %d", w.Code)
	}
}

func TestInviteLink_InviteOnlyRegistrationRequiresTheToken(t *testing.T) {
	f := newInviteFixture(t, true)
	token := f.invite(t, inviteeEmail, domain.OrgRoleViewer)

	if w := f.registerAs("carol", inviteeEmail, ""); w.Code != http.StatusForbidden {
		t.Fatalf("invite-only without a token: got %d, want 403", w.Code)
	}
	if w := f.registerAs("carol", inviteeEmail, token); w.Code != http.StatusCreated {
		t.Fatalf("invite-only with the token: %d %s", w.Code, w.Body.String())
	}
	if role := f.roleOf(t, inviteeEmail); role != domain.OrgRoleViewer {
		t.Errorf("role = %q, want viewer", role)
	}
}

func TestInviteLink_AcceptNeedsASignedInCaller(t *testing.T) {
	f := newInviteFixture(t, false)
	token := f.invite(t, inviteeEmail, domain.OrgRoleViewer)

	r := chi.NewRouter()
	r.Route(testOrgsPath, func(r chi.Router) {
		r.Use(auth.RequireAuth)
		NewOrgHandler(f.store, f.store).Routes(r, true)
	})
	req := httptest.NewRequest("POST", testOrgsPath+"/invites/accept", strings.NewReader(fmt.Sprintf(`{"token":%q}`, token)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous accept: got %d, want 401", w.Code)
	}
}
