package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func loginFrom(r chi.Router, remoteAddr, username, password string) int {
	req := httptest.NewRequest(http.MethodPost, routeLogin, strings.NewReader(loginBody(username, password)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// Someone guessing from their own address must not be able to lock the owner
// out from a different one.
func TestLoginLockFromOneAddressDoesNotLockTheOwnerElsewhere(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 3)
	for i := 0; i < 5; i++ {
		loginFrom(r, "203.0.113.9:4000", user, "wrong")
	}
	if code := loginFrom(r, "203.0.113.9:4000", user, password); code != http.StatusTooManyRequests {
		t.Fatalf("the guessing address must stay locked, got %d", code)
	}
	if code := loginFrom(r, "198.51.100.7:5000", user, password); code != http.StatusOK {
		t.Fatalf("the owner on another address must still sign in, got %d", code)
	}
}

// Twenty addresses with five wrong guesses each must not keep the owner out:
// the account slows down, and the right password from a fresh address still
// signs in once the delay has passed.
func TestLoginOwnerSignsInAfterTheDelayWhenManyAddressesGuess(t *testing.T) {
	r, h, user, password := slowedLoginRouter(t)
	for a := 0; a < 20; a++ {
		for g := 0; g < 5; g++ {
			loginFrom(r, fmt.Sprintf("203.0.113.%d:1", a+1), user, "wrong")
		}
	}
	h.slept = nil
	if code := loginFrom(r, "198.51.100.7:1", user, password); code != http.StatusOK {
		t.Fatalf("the owner from a fresh address got %d, want 200", code)
	}
	if len(h.slept) != 1 || h.slept[0] <= 0 {
		t.Fatalf("the owner's attempt was not delayed: %v", h.slept)
	}
	if h.slept[0] > 10*time.Second {
		t.Fatalf("the delay %s is not capped", h.slept[0])
	}
}

// An IPv6 host owns a whole /64, so its addresses share one budget.
func TestLoginCountsIPv6PerSlash64(t *testing.T) {
	r, _, user, password := slowedLoginRouter(t)
	for i := 0; i < 5; i++ {
		loginFrom(r, fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1), user, "wrong")
	}
	if code := loginFrom(r, "[2001:db8:1:2::ffff]:1", user, password); code != http.StatusTooManyRequests {
		t.Fatalf("another address in the same /64 got %d, want 429", code)
	}
	if code := loginFrom(r, "[2001:db8:1:3::1]:1", user, password); code != http.StatusOK {
		t.Fatalf("a different /64 got %d, want 200", code)
	}
}

func TestLoginSuccessOnlyClearsTheCallersAddress(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 2)
	loginFrom(r, "203.0.113.9:1", user, "wrong")
	loginFrom(r, "203.0.113.9:1", user, "wrong")
	if code := loginFrom(r, "198.51.100.7:1", user, password); code != http.StatusOK {
		t.Fatalf("owner: %d", code)
	}
	if code := loginFrom(r, "203.0.113.9:1", user, password); code != http.StatusTooManyRequests {
		t.Fatalf("the owner's success must not unlock the guesser, got %d", code)
	}
}

// slowedLoginRouter is guardedLoginRouter whose delays are recorded, not slept.
func slowedLoginRouter(t *testing.T) (chi.Router, *recordingSleep, string, string) {
	t.Helper()
	us := newMockUserStore()
	password := testutil.FixturePassword("carol-login")
	hash, _ := auth.HashPassword(password)
	now := time.Now()
	us.users["u1"] = &domain.User{ID: "u1", Username: "carol@example.test", PasswordHash: hash,
		Active: true, EmailVerifiedAt: &now, CreatedAt: &now}
	h := NewAuthHandler(us, newMockTokenStore())
	rec := &recordingSleep{}
	h.SetLoginSleep(rec.sleep)
	r := chi.NewRouter()
	r.Post(routeLogin, h.Login)
	return r, rec, "carol@example.test", password
}

type recordingSleep struct{ slept []time.Duration }

func (r *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	r.slept = append(r.slept, d)
	return nil
}
