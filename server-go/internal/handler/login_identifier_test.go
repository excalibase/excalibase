package handler

import (
	"net/http"
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

// A username is matched exactly; only an address ignores case.
func TestLoginDoesNotFoldTheUsername(t *testing.T) {
	r, _, password := identifierLoginRouter(t, 10)
	if w := doRequest(r, "POST", routeLogin, loginBody("ERIN-3FA9C1", password)); w.Code != http.StatusUnauthorized {
		t.Fatalf("username in another case: got %d, want 401", w.Code)
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
	original := checkPassword
	checkPassword = func(password, hash string) bool {
		calls++
		return original(password, hash)
	}
	t.Cleanup(func() { checkPassword = original })

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
