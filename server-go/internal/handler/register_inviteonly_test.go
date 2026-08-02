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
// satisfies the type, and implements only the methods Register touches on the
// subsequent-user path — the invite lookup plus the two resolvePendingInvites
// writes. (Tests below pre-seed an existing user so the registration under test
// is never the first/platform_admin one, which would call BootstrapDefaultOrg.)
type inviteOrgStore struct {
	storage.OrgStore
	invites []*domain.PendingInvite
}

func (s *inviteOrgStore) FindPendingInvitesByEmail(context.Context, string) ([]*domain.PendingInvite, error) {
	return s.invites, nil
}
func (s *inviteOrgStore) AddOrgMember(context.Context, *domain.OrgMember) error { return nil }
func (s *inviteOrgStore) DeletePendingInvite(context.Context, int64) error      { return nil }

// seededStore returns a user store that already has one user, so the next
// registration is a "subsequent" one (role=user), not the platform bootstrap.
func seededStore() *mockUserStore {
	us := newMockUserStore()
	now := time.Now()
	us.users["u0"] = &domain.User{ID: "u0", Username: "existing", Email: "existing@x.test", Role: "platform_admin", CreatedAt: &now}
	return us
}

func registerHandler(us *mockUserStore, org storage.OrgStore, inviteOnly bool) *AuthHandler {
	h := NewAuthHandler(us, newMockTokenStore())
	if org != nil {
		h.SetOrgStore(org)
	}
	h.SetInviteOnly(inviteOnly)
	return h
}

func postRegister(h *AuthHandler, username, email string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q}`, username, email, testutil.FixturePassword("reg-"+username))
	req := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/register", h.Register)
	r.ServeHTTP(w, req)
	return w
}

// EXC-329 hardening: in invite-only mode the FIRST registration still succeeds
// (it bootstraps the platform admin) — otherwise the platform is uninstallable.
func TestInviteOnly_FirstUserStillBootstraps(t *testing.T) {
	h := registerHandler(newMockUserStore(), nil, true) // empty store → first user → platform_admin
	if w := postRegister(h, "admin", "admin@x.test"); w.Code != http.StatusCreated {
		t.Fatalf("first registration must succeed in invite-only mode, got %d: %s", w.Code, w.Body.String())
	}
}

// A subsequent registration with no invite is rejected.
func TestInviteOnly_UninvitedRejected(t *testing.T) {
	h := registerHandler(seededStore(), &inviteOrgStore{}, true)
	w := postRegister(h, "mallory", "mallory@evil.test")
	if w.Code != http.StatusForbidden {
		t.Errorf("uninvited registration must be 403, got %d: %s", w.Code, w.Body.String())
	}
}

// A subsequent registration WITH a pending invite is allowed.
func TestInviteOnly_InvitedAllowed(t *testing.T) {
	org := &inviteOrgStore{invites: []*domain.PendingInvite{{ID: 1, OrgID: "org1", Role: "developer"}}}
	h := registerHandler(seededStore(), org, true)
	w := postRegister(h, "bob", "bob@company.test")
	if w.Code != http.StatusCreated {
		t.Errorf("invited registration must succeed, got %d: %s", w.Code, w.Body.String())
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
