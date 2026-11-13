package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// Rotating addresses must not buy unlimited guesses at one account.
func TestLoginAccountWideBudgetStopsGuessingAcrossAddresses(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 3)
	for i := 0; i < 400; i++ {
		loginFrom(r, fmt.Sprintf("10.1.%d.%d:1", i/200, i%200), user, "wrong")
	}
	if code := loginFrom(r, "192.0.2.77:1", user, password); code != http.StatusTooManyRequests {
		t.Fatalf("an account hit from many addresses must hit the account-wide cap, got %d", code)
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
