package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type fakeOwnerSuspender struct {
	userID, reason string
	err            error
	calls          int
}

func (f *fakeOwnerSuspender) Suspend(_ context.Context, _ adminconsole.Actor, userID, reason string) (*adminconsole.Result, error) {
	f.calls++
	f.userID, f.reason = userID, reason
	return &adminconsole.Result{}, f.err
}

func revokeRig(t *testing.T, suspender OwnerSuspender) (*AdminHandler, *adminOrgStore, *inMemoryInstanceStore, http.Handler) {
	t.Helper()
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-1": {ProjectID: "proj-1", OrgID: "org-9", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-1"},
	}}
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme", OwnerID: "owner-1"}}
	h := NewAdminHandler(newDockerProvSvc(t, store), store, orgStore, &captureAudit{}, nil, "", nil)
	h.SetOwnerSuspender(suspender)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	return h, orgStore, store, r
}

func revoke(r http.Handler, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/org-9?cascade=true"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// EXC-757: revoking an org also suspends its owner, so the owner cannot sign
// in and start again; the reason is required and travels with the suspension.
func TestAdmin_RevokeOrg_SuspendsTheOwnerWithTheReason(t *testing.T) {
	sus := &fakeOwnerSuspender{}
	_, orgs, _, r := revokeRig(t, sus)
	w := revoke(r, "&reason=proxy+abuse+on+every+project")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", w.Code, w.Body.String())
	}
	if sus.calls != 1 || sus.userID != "owner-1" {
		t.Fatalf("owner not suspended: %+v", sus)
	}
	if sus.reason == "" || !strings.Contains(sus.reason, "proxy abuse on every project") {
		t.Errorf("the reason did not reach the suspension: %q", sus.reason)
	}
	if !orgs.deleted {
		t.Error("the org should still be deleted")
	}
}

func TestAdmin_RevokeOrg_RefusesWithoutAReason(t *testing.T) {
	sus := &fakeOwnerSuspender{}
	_, orgs, store, r := revokeRig(t, sus)
	for _, q := range []string{"", "&reason=", "&reason=+++"} {
		if w := revoke(r, q); w.Code != http.StatusBadRequest {
			t.Errorf("query %q: got %d, want 400", q, w.Code)
		}
	}
	if sus.calls != 0 || orgs.deleted {
		t.Error("nothing may be suspended or deleted without a reason")
	}
	if got, _ := store.FindByProjectID("proj-1"); got == nil {
		t.Error("a project was dropped without a reason")
	}
}

// If the owner cannot be suspended, nothing is torn down: an org must not be
// destroyed while its owner stays free to sign in.
func TestAdmin_RevokeOrg_DoesNothingWhenTheOwnerCannotBeSuspended(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"protected owner": {adminconsole.ErrProtectedAccount, http.StatusConflict},
		"store failure":   {errors.New("db down"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			_, orgs, store, r := revokeRig(t, &fakeOwnerSuspender{err: tc.err})
			if w := revoke(r, "&reason=abuse+confirmed+by+logs"); w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if orgs.deleted {
				t.Error("org deleted though the owner was not suspended")
			}
			if got, _ := store.FindByProjectID("proj-1"); got == nil {
				t.Error("a project was dropped though the owner was not suspended")
			}
		})
	}
}

func TestAdmin_RevokeOrg_RefusesWhenNoSuspenderIsWired(t *testing.T) {
	_, orgs, _, r := revokeRig(t, nil)
	if w := revoke(r, "&reason=abuse+confirmed+by+logs"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	if orgs.deleted {
		t.Error("org deleted without a way to suspend its owner")
	}
}

// deactivatingStore is the suspension store over the same user the login
// handler reads: suspending flips it exactly as users.active does.
type deactivatingStore struct{ users *mockUserStore }

func (d deactivatingStore) FindUser(_ context.Context, id string) (*adminconsole.UserRef, error) {
	u := d.users.users[id]
	if u == nil {
		return nil, nil
	}
	return &adminconsole.UserRef{ID: u.ID, Email: u.Email, Role: u.Role, Active: u.Active}, nil
}
func (deactivatingStore) OwnedApps(context.Context, string) ([]adminconsole.OwnedApp, error) {
	return nil, nil
}
func (d deactivatingStore) Suspend(_ context.Context, s adminconsole.Suspension, _ []adminconsole.SuspendedApp) error {
	d.users.users[s.UserID].Active = false
	return nil
}
func (deactivatingStore) Unsuspend(context.Context, string) error { return nil }
func (deactivatingStore) SuspendedApps(context.Context, string) ([]adminconsole.SuspendedApp, error) {
	return nil, nil
}
func (deactivatingStore) ForgetSuspendedApp(context.Context, string, string, string) error {
	return nil
}

type noApps struct{}

func (noApps) PauseApp(context.Context, string, string) (*apphost.App, error) { return nil, nil }
func (noApps) ResumeApp(context.Context, string, string, string) (*apphost.App, error) {
	return nil, nil
}

// An org's owner signs in fine until the org is revoked; after, the same
// correct password is refused.
func TestAdmin_RevokeOrg_ThenTheOwnersLoginIsRefused(t *testing.T) {
	users := newMockUserStore()
	owner := testutil.FixtureToken("owner")
	password := testutil.FixturePassword("owner-login")
	hash, _ := auth.HashPassword(password)
	now := time.Now()
	users.users["owner-1"] = &domain.User{ID: "owner-1", Username: owner, Email: "owner@acme.test", Role: "user", PasswordHash: hash, Active: true, EmailVerifiedAt: &now, CreatedAt: &now}
	login := setupAuthRouter(t, users, newMockTokenStore())
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, owner, password)

	if w := doRequest(login, "POST", routeLogin, body); w.Code != http.StatusOK {
		t.Fatalf("before the revoke the owner must sign in, got %d", w.Code)
	}

	svc := adminconsole.NewSuspensionService(deactivatingStore{users}, noApps{}, &captureAudit{})
	_, _, _, r := revokeRig(t, svc)
	if w := revoke(r, "&reason=confirmed+abuse+across+projects"); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	w := doRequest(login, "POST", routeLogin, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("after the revoke the owner's login got %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Error("a session was issued to a suspended owner")
	}
}
