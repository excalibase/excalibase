package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// Every request path that hashes or verifies a password shares one bound. A
// path that skipped it would let a flood run the pod out of memory, so each
// must refuse fast with 503 + Retry-After when the gate is full, and keep
// working once it frees up.
func TestEveryPasswordPathRefusesWhenTheGateIsFull(t *testing.T) {
	password := testutil.FixturePassword("gate-path")
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	now := time.Now()

	paths := map[string]func(t *testing.T) *http.Response{}
	_ = paths

	cases := []struct {
		name   string
		method string
		route  string
		body   string
		router func(t *testing.T) chi.Router
	}{
		{"login (verify)", "POST", routeLogin, loginBody("carol@example.test", password), func(t *testing.T) chi.Router {
			us := newMockUserStore()
			us.users["u1"] = &domain.User{ID: "u1", Username: "carol@example.test", PasswordHash: hash,
				Active: true, EmailVerifiedAt: &now, CreatedAt: &now}
			r := chi.NewRouter()
			r.Post(routeLogin, NewAuthHandler(us, newMockTokenStore()).Login)
			return r
		}},
		{"login of an unknown name (dummy verify)", "POST", routeLogin, loginBody("ghost@example.test", password), func(t *testing.T) chi.Router {
			r := chi.NewRouter()
			r.Post(routeLogin, NewAuthHandler(newMockUserStore(), newMockTokenStore()).Login)
			return r
		}},
		{"register (hash)", "POST", "/api/auth/register",
			fmt.Sprintf(`{"username":"alice","email":"alice@test.com","password":%q,"setupToken":"tok"}`, password),
			func(t *testing.T) chi.Router {
				us := newMockUserStore()
				h := NewAuthHandler(us, newMockTokenStore())
				h.SetSetupTokenStore(newMockSetupTokenStore(us))
				r := chi.NewRouter()
				r.Post("/api/auth/register", h.Register)
				return r
			}},
		{"admin create user (hash)", "POST", routeAuthUsers,
			fmt.Sprintf(`{"username":"newuser","email":"new@example.com","password":%q,"role":"platform_operator"}`, password),
			func(t *testing.T) chi.Router { return setupAuthRouter(t, newMockUserStore(), newMockTokenStore()) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.router(t)
			restore := auth.ConfigurePasswordGate(0, time.Millisecond)
			begin := time.Now()
			w := doRequest(r, tc.method, tc.route, tc.body)
			restore()

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("full gate: got %d %s, want 503", w.Code, w.Body.String())
			}
			if w.Header().Get("Retry-After") == "" {
				t.Error("a refused password check must say when to retry")
			}
			if time.Since(begin) > time.Second {
				t.Errorf("refusal took %v, must be fast", time.Since(begin))
			}
		})
	}
}

// A refusal for load must not read as a wrong password, or it would burn the
// account's failed-attempt budget during a flood.
func TestBusyGateDoesNotCountAgainstTheAccount(t *testing.T) {
	r, user, password := guardedLoginRouter(t, 2)
	restore := auth.ConfigurePasswordGate(0, time.Millisecond)
	for i := 0; i < 5; i++ {
		if w := doRequest(r, "POST", routeLogin, loginBody(user, password)); w.Code != http.StatusServiceUnavailable {
			restore()
			t.Fatalf("attempt %d: got %d, want 503", i, w.Code)
		}
	}
	restore()
	if w := doRequest(r, "POST", routeLogin, loginBody(user, password)); w.Code != http.StatusOK {
		t.Fatalf("after the load passed: got %d %s, want 200", w.Code, w.Body.String())
	}
}

// A sign-in flood is answered, not queued: with fewer slots than requests every
// reply is a verdict (401) or a fast refusal (503), and the burst finishes
// within a few waits rather than one hash per request in series.
func TestSignInFloodIsBoundedAndNeverHangs(t *testing.T) {
	r, user, _ := guardedLoginRouter(t, 1000)
	restore := auth.ConfigurePasswordGate(2, 50*time.Millisecond)
	defer restore()

	const flood = 40
	codes := make(chan int, flood)
	begin := time.Now()
	for i := 0; i < flood; i++ {
		go func() { codes <- doRequest(r, "POST", routeLogin, loginBody(user, "wrong-pass")).Code }()
	}
	refused := 0
	for i := 0; i < flood; i++ {
		switch code := <-codes; code {
		case http.StatusUnauthorized:
		case http.StatusServiceUnavailable:
			refused++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if refused == 0 {
		t.Error("a flood past the slots was never refused")
	}
	if time.Since(begin) > 10*time.Second {
		t.Errorf("flood took %v", time.Since(begin))
	}
}
