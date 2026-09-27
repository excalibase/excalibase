package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/loginguard"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func guardedLoginRouter(t *testing.T, limit int) (chi.Router, string, string) {
	t.Helper()
	us := newMockUserStore()
	password := testutil.FixturePassword("carol-login")
	hash, _ := auth.HashPassword(password)
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID: "u1", Username: "carol@example.test", PasswordHash: hash,
		Active: true, EmailVerifiedAt: &now, CreatedAt: &now,
	}
	h := NewAuthHandler(us, newMockTokenStore())
	h.SetLoginGuard(loginguard.New(limit, time.Hour))
	r := chi.NewRouter()
	r.Post(routeLogin, h.Login)
	return r, "carol@example.test", password
}

func loginBody(username, password string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
}

func TestLoginLocksAnAccountAfterRepeatedFailures(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 3)
	for i := 0; i < 3; i++ {
		if w := doRequest(r, "POST", routeLogin, loginBody(user, "wrong")); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: got %d, want 401", i+1, w.Code)
		}
	}
	w := doRequest(r, "POST", routeLogin, loginBody(user, password))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked account with the right password: got %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a locked sign-in must say when to retry")
	}
}

// The lock applies whether or not the name exists, so a 429 confirms nothing.
func TestLoginLockDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	r, _, _ := guardedLoginRouter(t, 2)
	codes := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		codes = append(codes, doRequest(r, "POST", routeLogin, loginBody("ghost@example.test", "wrong")).Code)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("unknown name codes %v; the third must be 429 like a real account", codes)
	}
}

func TestLoginSuccessClearsEarlierFailures(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 2)
	doRequest(r, "POST", routeLogin, loginBody(user, "wrong"))
	if w := doRequest(r, "POST", routeLogin, loginBody(user, password)); w.Code != http.StatusOK {
		t.Fatalf("login inside the budget: got %d", w.Code)
	}
	doRequest(r, "POST", routeLogin, loginBody(user, "wrong"))
	if w := doRequest(r, "POST", routeLogin, loginBody(user, password)); w.Code != http.StatusOK {
		t.Fatalf("earlier failures still counted after a success: got %d", w.Code)
	}
}

func TestLoginIsGuardedWithoutExplicitWiring(t *testing.T) {
	us := newMockUserStore()
	r := chi.NewRouter()
	r.Post(routeLogin, NewAuthHandler(us, newMockTokenStore()).Login)
	last := 0
	for i := 0; i < 50; i++ {
		last = doRequest(r, "POST", routeLogin, loginBody("dave@example.test", "wrong")).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("50 guesses at one account answered %d at the end; the default guard must stop them", last)
	}
}
