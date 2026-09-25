package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// inviteOrgStore is a minimal OrgStore: it embeds the interface (nil) so it
// satisfies the type, and implements only the invite methods Register touches.
// (Tests below pre-seed an existing user so the registration under test is
// never the first/platform_admin one, which would call BootstrapDefaultOrg.)
type inviteOrgStore struct {
	storage.OrgStore
	tokenHash string
	accepted  []string
	acceptErr error
	createErr error
	created   []*domain.PendingInvite
}

func (s *inviteOrgStore) CreatePendingInvite(_ context.Context, inv *domain.PendingInvite) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, inv)
	return nil
}

func (s *inviteOrgStore) FindPendingInviteByToken(_ context.Context, hash string, _ time.Time) (*domain.PendingInvite, error) {
	if s.tokenHash == "" || hash != s.tokenHash {
		return nil, storage.ErrInviteInvalid
	}
	return &domain.PendingInvite{ID: 1, OrgID: "org1", Role: "developer"}, nil
}

func (s *inviteOrgStore) AcceptPendingInvite(ctx context.Context, hash, userID string, now time.Time) (*domain.PendingInvite, error) {
	if s.acceptErr != nil {
		return nil, s.acceptErr
	}
	inv, err := s.FindPendingInviteByToken(ctx, hash, now)
	if err != nil {
		return nil, err
	}
	s.tokenHash = ""
	s.accepted = append(s.accepted, userID)
	return inv, nil
}

// seededStore returns a user store that already has one user, so the next
// registration is a "subsequent" one (role=user), not the platform bootstrap.
func seededStore() *mockUserStore {
	us := newMockUserStore()
	now := time.Now()
	us.users["u0"] = &domain.User{ID: "u0", Username: "existing", Email: "existing@x.test", Role: "platform_admin", CreatedAt: &now}
	return us
}

// registerHandler wires an AuthHandler for Register tests. Every case here
// pre-seeds an existing admin (via seededStore), so the registration under
// test is always the "subsequent" path — the first-admin/setup-token path is
// covered in setup_token_test.go.
func registerHandler(us *mockUserStore, org storage.OrgStore, inviteOnly bool) *AuthHandler {
	h := NewAuthHandler(us, newMockTokenStore())
	h.SetSetupTokenStore(newMockSetupTokenStore(us))
	if org != nil {
		h.SetOrgStore(org)
	}
	h.SetInviteOnly(inviteOnly)
	return h
}

func postRegister(h *AuthHandler, username, email string) *httptest.ResponseRecorder {
	return postRegisterWithToken(h, username, email, "")
}

// postRegisterWithToken sends a registration carrying a setupToken
// (EXC-451's first-admin path).
func postRegisterWithToken(h *AuthHandler, username, email, setupToken string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q,"setupToken":%q}`,
		username, email, testutil.FixturePassword("reg-"+username), setupToken)
	return doRegisterRequest(h, body)
}

// postRegisterWithInvite sends a registration carrying an inviteToken
// (EXC-468's subsequent-user path).
func postRegisterWithInvite(h *AuthHandler, username, email, token string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q,"inviteToken":%q}`,
		username, email, testutil.FixturePassword("reg-"+username), token)
	return doRegisterRequest(h, body)
}

func doRegisterRequest(h *AuthHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/register", h.Register)
	r.ServeHTTP(w, req)
	return w
}

// A subsequent registration with no invite is rejected.
func TestInviteOnly_UninvitedRejected(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{}, true)
	w := postRegister(h, "mallory", "mallory@evil.test")
	if w.Code != http.StatusForbidden {
		t.Errorf("uninvited registration must be 403, got %d: %s", w.Code, w.Body.String())
	}
}

// A subsequent registration carrying a live invite token is allowed and joins.
func TestInviteOnly_InvitedAllowed(t *testing.T) {
	const token = "invite-token"
	org := &inviteOrgStore{tokenHash: hashToken(token)}
	h := registerHandler(seededStore(), org, true)
	w := postRegisterWithInvite(h, "bob", "bob@company.test", token)
	if w.Code != http.StatusCreated {
		t.Errorf("invited registration must succeed, got %d: %s", w.Code, w.Body.String())
	}
	if len(org.accepted) != 1 {
		t.Errorf("the invite must be spent once, got %d", len(org.accepted))
	}
}

// Knowing an invited address is not enough: without the token it is refused.
func TestInviteOnly_InvitedEmailWithoutTokenRejected(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{tokenHash: hashToken("invite-token")}, true)
	if w := postRegister(h, "bob", "bob@company.test"); w.Code != http.StatusForbidden {
		t.Errorf("registration without the token must be 403, got %d", w.Code)
	}
}

// A wrong token is refused before any account exists.
func TestRegister_WrongInviteTokenCreatesNoAccount(t *testing.T) {
	us := seededStore()
	h := registerHandler(us, &inviteOrgStore{tokenHash: hashToken("invite-token")}, false)
	if w := postRegisterWithInvite(h, "eve", "eve@x.test", "not-the-token"); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong token must be 400, got %d", w.Code)
	}
	if len(us.users) != 1 {
		t.Errorf("a refused invite created an account")
	}
}

// With no org store wired, a token cannot be honoured.
func TestRegister_InviteTokenWithoutOrgStoreRefused(t *testing.T) {
	h := registerHandler(seededStore(), nil, false)
	if w := postRegisterWithInvite(h, "eve", "eve@x.test", "any"); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

// Default (open) mode is unchanged: an uninvited subsequent user may register.
func TestOpenMode_UninvitedAllowed(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{}, false)
	w := postRegister(h, "carol", "carol@x.test")
	if w.Code != http.StatusCreated {
		t.Errorf("open mode must allow uninvited registration, got %d: %s", w.Code, w.Body.String())
	}
}
