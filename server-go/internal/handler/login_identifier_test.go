package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/loginguard"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// identifierLoginRouter holds one verified account that joined through a
// provider: its username is generated and its address is mixed case.
func identifierLoginRouter(t *testing.T, limit int) (chi.Router, *domain.User, string) {
	t.Helper()
	us := newMockUserStore()
	password := testutil.FixturePassword("erin-login")
	hash, _ := auth.HashPassword(password)
	now := time.Now()
	user := &domain.User{
		ID: "u-erin", Username: "erin-3fa9c1", Email: "Erin@Example.test", PasswordHash: hash,
		Active: true, Kind: domain.UserKindHuman, EmailVerifiedAt: &now, CreatedAt: &now,
	}
	us.users[user.ID] = user
	h := NewAuthHandler(us, newMockTokenStore())
	h.SetLoginGuard(loginguard.New(limit, time.Hour))
	r := chi.NewRouter()
	r.Post(routeLogin, h.Login)
	return r, user, password
}

func TestLoginAcceptsTheAccountsEmailInAnyCase(t *testing.T) {
	r, _, password := identifierLoginRouter(t, 10)
	if w := doRequest(r, "POST", routeLogin, loginBody("erin@example.TEST", password)); w.Code != http.StatusOK {
		t.Fatalf("sign-in by e-mail: got %d %s", w.Code, w.Body.String())
	}
}

func TestLoginStillAcceptsTheUsername(t *testing.T) {
	r, user, password := identifierLoginRouter(t, 10)
	if w := doRequest(r, "POST", routeLogin, loginBody(user.Username, password)); w.Code != http.StatusOK {
		t.Fatalf("sign-in by username: got %d %s", w.Code, w.Body.String())
	}
}

// A username matches ignoring case, as an address does: no two accounts differ only by case.
func TestLoginFoldsTheUsername(t *testing.T) {
	r, _, password := identifierLoginRouter(t, 10)
	if w := doRequest(r, "POST", routeLogin, loginBody("ERIN-3FA9C1", password)); w.Code != http.StatusOK {
		t.Fatalf("username in another case: got %d, want 200", w.Code)
	}
}

func TestLoginAnswersAnUnknownAddressLikeAWrongPassword(t *testing.T) {
	r, user, _ := identifierLoginRouter(t, 10)
	unknown := doRequest(r, "POST", routeLogin, loginBody("nobody@example.test", "wrong-pass"))
	wrong := doRequest(r, "POST", routeLogin, loginBody(user.Email, "wrong-pass"))
	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("codes unknown=%d wrong=%d, want 401 for both", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("bodies differ:\nunknown: %s\nwrong:   %s", unknown.Body.String(), wrong.Body.String())
	}
}

// Every refusal pays for one password hash, so the answer time does not tell
// a registered identifier from an unknown one.
func TestLoginHashesThePasswordEvenForAnUnknownIdentifier(t *testing.T) {
	r, _, _ := identifierLoginRouter(t, 10)
	calls := 0
	original := verifyPassword
	verifyPassword = func(password, hash, client string) (bool, error) {
		calls++
		return original(password, hash, client)
	}
	t.Cleanup(func() { verifyPassword = original })

	for _, identifier := range []string{"nobody@example.test", "nobody"} {
		calls = 0
		doRequest(r, "POST", routeLogin, loginBody(identifier, "wrong-pass"))
		if calls != 1 {
			t.Errorf("%q: password checked %d times, want 1", identifier, calls)
		}
	}
}

func TestLoginRefusesAServicePrincipalByItsAddress(t *testing.T) {
	r, user, password := identifierLoginRouter(t, 10)
	user.Kind = domain.UserKindService
	if w := doRequest(r, "POST", routeLogin, loginBody(user.Email, password)); w.Code != http.StatusUnauthorized {
		t.Fatalf("service principal by address: got %d, want 401", w.Code)
	}
}

// Switching between the username and the address does not buy more guesses.
func TestLoginGuessBudgetIsSharedByUsernameAndEmail(t *testing.T) {
	r, user, password := identifierLoginRouter(t, 2)
	doRequest(r, "POST", routeLogin, loginBody(user.Username, "wrong"))
	doRequest(r, "POST", routeLogin, loginBody(user.Email, "wrong"))
	if w := doRequest(r, "POST", routeLogin, loginBody("ERIN@example.test", password)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third attempt by another identifier: got %d, want 429", w.Code)
	}
}

func recordedLoginFrom(r chi.Router, ip, username, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", routeLogin, strings.NewReader(loginBody(username, password)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// holdSlotsFor makes every password check from client keep its gate slot for
// hold, so a flood from it really saturates the gate instead of finishing as
// fast as the CPU allows.
func holdSlotsFor(t *testing.T, client string, hold time.Duration) {
	t.Helper()
	original := verifyPassword
	verifyPassword = func(password, hash, from string) (bool, error) {
		if from == client {
			if err := auth.RunInPasswordGate(from, func() { time.Sleep(hold) }); err != nil {
				return false, err
			}
		}
		return original(password, hash, from)
	}
	t.Cleanup(func() { verifyPassword = original })
}

// floodFrom keeps one client address busy with sign-ins for unknown names until
// the returned stop is called.
func floodFrom(r chi.Router, ip string, workers int) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; ; attempt++ {
				select {
				case <-done:
					return
				default:
				}
				recordedLoginFrom(r, ip, fmt.Sprintf("ghost-%d-%d", worker, attempt), "wrong-pass")
			}
		}()
	}
	return func() { close(done); wg.Wait() }
}

// One flooding address cannot fill the gate: a real admin signing in from
// another address still gets in (EXC-749).
func TestLoginFromAnotherAddressSucceedsDuringAFlood(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 1_000_000)
	defer auth.ConfigurePasswordGate(4, 100*time.Millisecond)()
	recordedLoginFrom(r, "203.0.113.1", "warm-up", "wrong-pass") // cold start of the cached dummy hash
	holdSlotsFor(t, "203.0.113.1", 150*time.Millisecond)
	defer floodFrom(r, "203.0.113.1", 32)()
	time.Sleep(50 * time.Millisecond)

	for attempt := 0; attempt < 20; attempt++ {
		if w := recordedLoginFrom(r, "198.51.100.7", user, password); w.Code != http.StatusOK {
			t.Fatalf("sign-in %d from a quiet address during the flood: got %d %s, want 200", attempt, w.Code, w.Body.String())
		}
	}
}

// The refusal must not say whether an account exists: during a flood, wrong
// passwords for a real account and for unknown names are refused alike.
func TestBusyRefusalDoesNotTellARealAccountFromAnUnknownName(t *testing.T) {
	r, user, _ := guardedLoginRouter(t, 1_000_000)
	defer auth.ConfigurePasswordGate(2, 100*time.Millisecond)()
	recordedLoginFrom(r, "203.0.113.1", "warm-up", "wrong-pass")
	defer floodFrom(r, "203.0.113.1", 32)()
	time.Sleep(50 * time.Millisecond)

	const probes = 30
	refused := map[string]int{}
	for attempt := 0; attempt < probes; attempt++ {
		for name, identifier := range map[string]string{"known": user, "unknown": fmt.Sprintf("nobody-%d", attempt)} {
			w := recordedLoginFrom(r, "203.0.113.1", identifier, "wrong-pass")
			switch w.Code {
			case http.StatusServiceUnavailable:
				if w.Header().Get("Retry-After") == "" {
					t.Fatal("a busy refusal without Retry-After")
				}
				refused[name]++
			case http.StatusUnauthorized:
			default:
				t.Fatalf("%s: unexpected %d", name, w.Code)
			}
		}
	}
	if diff := refused["known"] - refused["unknown"]; diff > 8 || diff < -8 {
		t.Fatalf("refusals differ by account: known %d, unknown %d of %d", refused["known"], refused["unknown"], probes)
	}
}
