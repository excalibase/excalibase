package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// --- Password ---

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPassword("secret123", hash) {
		t.Error("valid password should verify")
	}
	if CheckPassword("wrong", hash) {
		t.Error("wrong password should not verify")
	}
}

// --- PAT ---

func TestGenerateToken(t *testing.T) {
	raw := GenerateToken()
	if len(raw) < 32 {
		t.Errorf("token too short: %d", len(raw))
	}

	hash := HashToken(raw)
	if hash == raw {
		t.Error("hash should differ from raw")
	}
	if HashToken(raw) != hash {
		t.Error("same token should produce same hash")
	}
}

func TestTokenPrefix(t *testing.T) {
	raw := GenerateToken()
	prefix := TokenPrefix(raw)
	if len(prefix) != 12 {
		t.Errorf("prefix should be 12 chars, got %d", len(prefix))
	}
	if prefix != raw[:12] {
		t.Error("prefix should match first 12 chars")
	}
}

// --- RBAC ---

func TestRBACPermissions(t *testing.T) {
	if !HasPermission("admin", PermProvision) {
		t.Error("admin should have provision permission")
	}
	if !HasPermission("operator", PermProvision) {
		t.Error("operator should have provision permission")
	}
	if HasPermission("viewer", PermProvision) {
		t.Error("viewer should NOT have provision permission")
	}
	if !HasPermission("viewer", PermViewInstances) {
		t.Error("viewer should have view permission")
	}
	if HasPermission("viewer", PermManageUsers) {
		t.Error("viewer should NOT manage users")
	}
	if !HasPermission("admin", PermManageUsers) {
		t.Error("admin should manage users")
	}
}

// --- Middleware ---

type mockTokenLookup struct {
	tokens map[string]*domain.AccessToken // keyed by token_hash
	users  map[string]*domain.User        // keyed by user ID
}

func (m *mockTokenLookup) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	return m.tokens[hash], nil
}

func (m *mockTokenLookup) FindUserByID(ctx context.Context, id string) (*domain.User, error) {
	return m.users[id], nil
}

func TestMiddlewareNoAuth(t *testing.T) {
	lookup := &mockTokenLookup{
		tokens: map[string]*domain.AccessToken{},
		users:  map[string]*domain.User{},
	}

	r := chi.NewRouter()
	r.Use(ExtractAuth(lookup))
	r.With(RequireAuth).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/protected", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddlewareWithToken(t *testing.T) {
	raw := GenerateToken()
	hash := HashToken(raw)

	lookup := &mockTokenLookup{
		tokens: map[string]*domain.AccessToken{hash: {TokenHash: hash, UserID: "u1"}},
		users:  map[string]*domain.User{"u1": {ID: "u1", Username: "admin", Role: "admin", Active: true}},
	}

	r := chi.NewRouter()
	r.Use(ExtractAuth(lookup))
	r.With(RequireAuth).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r.Context())
		json.NewEncoder(w).Encode(map[string]string{"user": user.Username, "role": user.Role})
	})

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestMiddlewareInactiveUser(t *testing.T) {
	raw := GenerateToken()
	hash := HashToken(raw)

	lookup := &mockTokenLookup{
		tokens: map[string]*domain.AccessToken{hash: {TokenHash: hash, UserID: "u1"}},
		users:  map[string]*domain.User{"u1": {ID: "u1", Role: "admin", Active: false}},
	}

	r := chi.NewRouter()
	r.Use(ExtractAuth(lookup))
	r.With(RequireAuth).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should not reach"))
	})

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("inactive user should get 401, got %d", w.Code)
	}
}

func TestMiddlewareRequirePermission(t *testing.T) {
	raw := GenerateToken()
	hash := HashToken(raw)

	lookup := &mockTokenLookup{
		tokens: map[string]*domain.AccessToken{hash: {TokenHash: hash, UserID: "u1"}},
		users:  map[string]*domain.User{"u1": {ID: "u1", Role: "viewer", Active: true}},
	}

	r := chi.NewRouter()
	r.Use(ExtractAuth(lookup))
	r.With(RequireAuth, RequirePermission(PermProvision)).Post("/provision", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("provisioned"))
	})

	req := httptest.NewRequest("POST", "/provision", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("viewer should be forbidden, got %d", w.Code)
	}
}

func TestMiddlewareInvalidToken(t *testing.T) {
	lookup := &mockTokenLookup{
		tokens: map[string]*domain.AccessToken{},
		users:  map[string]*domain.User{},
	}

	r := chi.NewRouter()
	r.Use(ExtractAuth(lookup))
	r.With(RequireAuth).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("no"))
	})

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer garbage-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid token should get 401, got %d", w.Code)
	}
}
