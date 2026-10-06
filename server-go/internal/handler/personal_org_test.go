package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// recordingPersonalOrgs answers EnsurePersonalOrg and records who asked.
type recordingPersonalOrgs struct {
	mu    sync.Mutex
	users []string
	err   error
}

func (s *recordingPersonalOrgs) EnsurePersonalOrg(_ context.Context, org *domain.Org) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = append(s.users, org.OwnerID)
	if s.err != nil {
		return false, s.err
	}
	return true, nil
}

func verifiedLoginUser(t *testing.T, us *mockUserStore) (username, password string) {
	t.Helper()
	username = testutil.FixtureToken("ada")
	password = testutil.FixturePassword("ada-login")
	hash, _ := auth.HashPassword(password)
	now := time.Now()
	us.users["u-ada"] = &domain.User{
		ID: "u-ada", Username: username, PasswordHash: hash, Role: "user",
		Active: true, Kind: domain.UserKindHuman, EmailVerifiedAt: &now, CreatedAt: &now,
	}
	return username, password
}

func loginWithPersonalOrgs(t *testing.T, orgs *recordingPersonalOrgs) (*httptest.ResponseRecorder, *mockTokenStore) {
	t.Helper()
	us, ts := newMockUserStore(), newMockTokenStore()
	username, password := verifiedLoginUser(t, us)
	h := NewAuthHandler(us, ts)
	h.SetPersonalOrgs(orgs)
	r := chi.NewRouter()
	r.Post(routeLogin, h.Login)
	return doRequest(r, "POST", routeLogin, fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)), ts
}

// EXC-553: signing in is when an account becomes usable, so that is when it
// gets its personal organization.
func TestLogin_EnsuresThePersonalOrg(t *testing.T) {
	orgs := &recordingPersonalOrgs{}
	w, _ := loginWithPersonalOrgs(t, orgs)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	if len(orgs.users) != 1 || orgs.users[0] != "u-ada" {
		t.Fatalf("personal org asked for %v", orgs.users)
	}
}

// An account that would land with no organization is not signed in; the next
// attempt retries the same idempotent step.
func TestLogin_RefusesTheSessionWhenThePersonalOrgCannotBeMade(t *testing.T) {
	orgs := &recordingPersonalOrgs{err: errors.New("db down")}
	w, ts := loginWithPersonalOrgs(t, orgs)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("login: got %d, want 500: %s", w.Code, w.Body.String())
	}
	if len(ts.tokens) != 0 {
		t.Fatal("a session was stored")
	}
	if c := sessionCookie(w); c != nil {
		t.Fatal("a session cookie was set")
	}
	if strings.Contains(w.Body.String(), "db down") {
		t.Fatalf("internal error leaked: %s", w.Body.String())
	}
}

func TestOAuthNewPersonGetsAPersonalOrg(t *testing.T) {
	h := newOAuthHarness(t, false)
	orgs := &recordingPersonalOrgs{}
	h.auth.SetPersonalOrgs(orgs)

	w := h.callback("st4te")
	if loc := redirectedTo(t, w); loc.Path != "/oauth/complete" {
		t.Fatalf("redirected to %s", loc)
	}
	user, _ := h.identities.FindUserByIdentity(context.Background(), "github", "4242")
	if user == nil || len(orgs.users) != 1 || orgs.users[0] != user.ID {
		t.Fatalf("personal org asked for %v, account %+v", orgs.users, user)
	}
}

func TestOAuthSignInRefusedWhenThePersonalOrgCannotBeMade(t *testing.T) {
	h := newOAuthHarness(t, false)
	h.auth.SetPersonalOrgs(&recordingPersonalOrgs{err: errors.New("db down")})

	w := h.callback("st4te")
	if loc := redirectedTo(t, w); loc.Query().Get("oauth_error") != "failed" {
		t.Fatalf("redirected to %s", loc)
	}
	if sessionCookie(w) != nil {
		t.Fatal("a session was issued")
	}
}

func TestRegister_FirstAdminGetsAPersonalOrgCheck(t *testing.T) {
	h, raw := firstAdminHandler(t)
	orgs := &recordingPersonalOrgs{}
	h.SetPersonalOrgs(orgs)

	if w := postRegisterWithToken(h, "founder", "founder@x.test", raw); w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	if len(orgs.users) != 1 {
		t.Fatalf("personal org asked for %v", orgs.users)
	}
}

// Org creation and the owner's membership are one write: a failure is
// reported, never answered with a created org that has no owner.
func TestCreateOrg_StoreFailureIsReported(t *testing.T) {
	orgs := fakestore.NewOrgs()
	orgs.Err = errors.New("db down")
	h := NewOrgHandler(orgs, fakestore.NewUsers())
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user := &domain.User{ID: "u-1", Role: "user", Active: true}
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), user)))
		})
	})
	r.Route("/api/orgs", func(r chi.Router) { h.Routes(r, true) })

	w := doRequest(r, "POST", "/api/orgs/", `{"name":"Acme","slug":"acme"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: %s", w.Code, w.Body.String())
	}
}
