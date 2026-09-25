package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// mockSetupTokenStore is an in-memory storage.SetupTokenStore. It holds its
// own lock across the whole of CreateFirstAdmin, the same way the Postgres
// implementation's row-level DELETE lock serializes racing callers, so it can
// stand in for concurrency tests without a real database.
type mockSetupTokenStore struct {
	mu        sync.Mutex
	users     *mockUserStore
	tokenHash string
}

func newMockSetupTokenStore(us *mockUserStore) *mockSetupTokenStore {
	return &mockSetupTokenStore{users: us}
}

func (s *mockSetupTokenStore) HasPlatformAdmin(ctx context.Context) (bool, error) {
	users, err := s.users.FindAllUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.Role == "platform_admin" {
			return true, nil
		}
	}
	return false, nil
}

func (s *mockSetupTokenStore) StoreSetupTokenHash(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenHash = tokenHash
	return nil
}

func (s *mockSetupTokenStore) CreateFirstAdmin(_ context.Context, tokenHash string, user *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenHash == "" || s.tokenHash != tokenHash {
		return storage.ErrInvalidSetupToken
	}
	s.tokenHash = ""
	return s.users.CreateUser(context.Background(), user)
}

// firstAdminHandler wires an AuthHandler over an empty user store with a
// setup token already generated and stored, mirroring what startup does.
func firstAdminHandler(t *testing.T) (*AuthHandler, string) {
	t.Helper()
	us := newMockUserStore()
	sts := newMockSetupTokenStore(us)
	raw := auth.GenerateSetupToken()
	if err := sts.StoreSetupTokenHash(t.Context(), auth.HashToken(raw)); err != nil {
		t.Fatalf("seed setup token: %v", err)
	}
	h := NewAuthHandler(us, newMockTokenStore())
	h.SetSetupTokenStore(sts)
	return h, raw
}

func TestRegister_FirstAdmin_NoToken_Forbidden(t *testing.T) {
	h, _ := firstAdminHandler(t)
	w := postRegisterWithToken(h, "founder", "founder@x.test", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("no setup token: got %d, want %d: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestRegister_FirstAdmin_WrongToken_Forbidden(t *testing.T) {
	h, _ := firstAdminHandler(t)
	w := postRegisterWithToken(h, "founder", "founder@x.test", "not-the-right-token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong setup token: got %d, want %d: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestRegister_FirstAdmin_RightToken_CreatesAdminAndBurnsToken(t *testing.T) {
	h, raw := firstAdminHandler(t)
	w := postRegisterWithToken(h, "founder", "founder@x.test", raw)
	if w.Code != http.StatusCreated {
		t.Fatalf("register with valid setup token: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	us := h.userStore.(*mockUserStore)
	var founder *domain.User
	for _, u := range us.users {
		if u.Username == "founder" {
			founder = u
		}
	}
	if founder == nil {
		t.Fatal("founder user was not created")
	}
	if founder.Role != "platform_admin" {
		t.Errorf("role: got %q, want platform_admin", founder.Role)
	}

	sts := h.setupTokens.(*mockSetupTokenStore)
	if sts.tokenHash != "" {
		t.Error("setup token was not burned after a successful first-admin registration")
	}
}

// After the first admin is created, a second registration presenting the
// SAME (now-burned) token must not become admin — it is treated as an
// ordinary subsequent registration.
func TestRegister_SecondAttempt_SameToken_NotAdmin(t *testing.T) {
	h, raw := firstAdminHandler(t)
	if w := postRegisterWithToken(h, "founder", "founder@x.test", raw); w.Code != http.StatusCreated {
		t.Fatalf("first registration: got %d: %s", w.Code, w.Body.String())
	}

	w2 := postRegisterWithToken(h, "impostor", "impostor@x.test", raw)
	if w2.Code != http.StatusCreated {
		t.Fatalf("second registration: got %d, want %d: %s", w2.Code, http.StatusCreated, w2.Body.String())
	}

	us := h.userStore.(*mockUserStore)
	for _, u := range us.users {
		if u.Username == "impostor" && u.Role == "platform_admin" {
			t.Fatal("second registration with the burned token must not become platform_admin")
		}
	}
}

// Two concurrent first registrations racing with the same valid token: the
// serialized burn (mockSetupTokenStore.mu, mirroring the Postgres row lock)
// must let exactly one of them become platform_admin.
func TestRegister_ConcurrentFirstRegistrations_ExactlyOneAdmin(t *testing.T) {
	h, raw := firstAdminHandler(t)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	names := []string{"racer-a", "racer-b"}
	for i := range names {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := postRegisterWithToken(h, names[i], names[i]+"@x.test", raw)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	// Both requests race to be "first": whichever loses the token-burn race
	// falls through to an ordinary (non-admin) registration rather than
	// failing outright, since by the time it runs an admin already exists.
	// Either way every request gets a definite, non-error outcome.
	for _, code := range codes {
		if code != http.StatusCreated && code != http.StatusForbidden {
			t.Errorf("unexpected status %d", code)
		}
	}

	us := h.userStore.(*mockUserStore)
	admins := 0
	for _, u := range us.users {
		if u.Role == "platform_admin" {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("expected exactly 1 platform_admin, got %d", admins)
	}
}

// The setup-token store must be wired for the first registration to ever
// succeed — an unwired server refuses loudly instead of silently minting an
// unguarded admin.
func TestRegister_FirstAdmin_SetupTokenStoreNotWired(t *testing.T) {
	h := NewAuthHandler(newMockUserStore(), newMockTokenStore())
	w := postRegisterWithToken(h, "founder", "founder@x.test", "any-token")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// A store failure while creating the first admin (distinct from an invalid
// token) surfaces as a plain 500, not a leaked internal error.
func TestRegister_FirstAdmin_StoreFailure_ServerError(t *testing.T) {
	h, raw := firstAdminHandler(t)
	h.userStore.(*mockUserStore).failSave = true
	w := postRegisterWithToken(h, "founder", "founder@x.test", raw)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// A store failure creating a subsequent (non-admin) user also surfaces as a
// plain 500.
func TestRegister_SubsequentUser_StoreFailure_ServerError(t *testing.T) {
	us := seededStore()
	h := registerHandler(us, &inviteOrgStore{}, false)
	us.failSave = true
	w := postRegister(h, "carol", "carol@x.test")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// A token-store failure after the account is created must not silently 200
// with a token the caller can never use again.
func TestRegister_TokenPersistenceFailure_ServerError(t *testing.T) {
	us := seededStore()
	ts := newMockTokenStore()
	ts.failSave = true
	h := NewAuthHandler(us, ts)
	h.SetSetupTokenStore(newMockSetupTokenStore(us))
	w := postRegister(h, "dave", "dave@x.test")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}
