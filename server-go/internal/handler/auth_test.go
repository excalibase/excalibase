package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// --- in-memory mock stores for auth tests ---

type mockUserStore struct {
	users    map[string]*domain.User
	failSave bool
}

func newMockUserStore() *mockUserStore {
	return &mockUserStore{users: make(map[string]*domain.User)}
}

func (s *mockUserStore) CreateUser(_ context.Context, u *domain.User) error {
	if s.failSave {
		return errors.New("db error")
	}
	s.users[u.ID] = u
	return nil
}

func (s *mockUserStore) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	return s.users[id], nil
}

func (s *mockUserStore) FindUserByUsername(_ context.Context, username string) (*domain.User, error) {
	for _, u := range s.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, nil
}

func (s *mockUserStore) FindUserByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

func (s *mockUserStore) FindAllUsers(_ context.Context) ([]*domain.User, error) {
	list := make([]*domain.User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	return list, nil
}

func (s *mockUserStore) DeleteUser(_ context.Context, id string) error {
	delete(s.users, id)
	return nil
}

type mockTokenStore struct {
	tokens   map[string]*domain.AccessToken
	failSave bool
}

func newMockTokenStore() *mockTokenStore {
	return &mockTokenStore{tokens: make(map[string]*domain.AccessToken)}
}

func (s *mockTokenStore) CreateToken(_ context.Context, t *domain.AccessToken) error {
	if s.failSave {
		return errors.New("db error")
	}
	s.tokens[t.TokenHash] = t
	return nil
}

func (s *mockTokenStore) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	return s.tokens[hash], nil
}

func (s *mockTokenStore) ListTokensByUser(_ context.Context, userID string) ([]*domain.AccessToken, error) {
	var result []*domain.AccessToken
	for _, tok := range s.tokens {
		if tok.UserID == userID {
			result = append(result, tok)
		}
	}
	return result, nil
}

func (s *mockTokenStore) DeleteToken(_ context.Context, tokenHash string) error {
	delete(s.tokens, tokenHash)
	return nil
}

// fakeLookup satisfies auth.TokenLookup for the ExtractAuth middleware.
type fakeLookup struct {
	token *domain.AccessToken
	user  *domain.User
	hash  string
}

func (f *fakeLookup) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	if hash == f.hash {
		return f.token, nil
	}
	return nil, nil
}

func (f *fakeLookup) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	if id == f.user.ID {
		return f.user, nil
	}
	return nil, nil
}

// setupAuthRouter wires up AuthHandler without any auth middleware
// (tests for unauthenticated behavior use the raw router directly).
func setupAuthRouter(t *testing.T, us *mockUserStore, ts *mockTokenStore) chi.Router {
	t.Helper()
	h := NewAuthHandler(us, ts)
	r := chi.NewRouter()
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", h.Login)
		r.Get("/me", h.Me)
		r.Get("/users", h.ListUsers)
		r.Post("/users", h.CreateUser)
		r.Delete("/users/{userId}", h.DeleteUser)
		r.Get("/tokens", h.ListTokens)
		r.Post("/tokens", h.CreateToken)
		r.Delete("/tokens/{tokenHash}", h.RevokeToken)
	})
	return r
}

// setupAuthRouterWithUser wires up AuthHandler with the ExtractAuth middleware
// so that requests carrying the returned rawToken will be authenticated as user.
func setupAuthRouterWithUser(
	t *testing.T,
	us *mockUserStore,
	ts *mockTokenStore,
	user *domain.User,
) (r chi.Router, rawToken string) {
	t.Helper()
	rawToken = "test-inject-token-abc"
	tokenHash := auth.HashToken(rawToken)

	lookup := &fakeLookup{
		hash:  tokenHash,
		token: &domain.AccessToken{TokenHash: tokenHash, UserID: user.ID},
		user:  user,
	}

	h := NewAuthHandler(us, ts)
	r = chi.NewRouter()
	r.Use(auth.ExtractAuth(lookup))
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", h.Login)
		r.Get("/me", h.Me)
		r.Get("/users", h.ListUsers)
		r.Post("/users", h.CreateUser)
		r.Delete("/users/{userId}", h.DeleteUser)
		r.Get("/tokens", h.ListTokens)
		r.Post("/tokens", h.CreateToken)
		r.Delete("/tokens/{tokenHash}", h.RevokeToken)
	})
	return r, rawToken
}

// doAuthRequest is like doRequest but adds an Authorization header.
func doAuthRequest(r chi.Router, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- Tests ---

func TestAuthLogin_Success(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()

	hash, _ := auth.HashPassword("hunter2")
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID:           "u1",
		Username:     "alice",
		PasswordHash: hash,
		Active:       true,
		CreatedAt:    &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", "/api/auth/login", `{"username":"alice","password":"hunter2"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("token should be present in response")
	}
}

func TestAuthLogin_InvalidCredentials_Returns401(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())

	w := doRequest(r, "POST", "/api/auth/login", `{"username":"nobody","password":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthLogin_WrongPassword_Returns401(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()

	hash, _ := auth.HashPassword("correctpass")
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID: "u1", Username: "bob", PasswordHash: hash, Active: true, CreatedAt: &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", "/api/auth/login", `{"username":"bob","password":"wrongpass"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthLogin_InvalidJSON_Returns400(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "POST", "/api/auth/login", "not json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAuthLogin_TokenCreationFails_Returns500(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	ts.failSave = true

	hash, _ := auth.HashPassword("pass")
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID: "u1", Username: "carol", PasswordHash: hash, Active: true, CreatedAt: &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", "/api/auth/login", `{"username":"carol","password":"pass"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestAuthMe_Authenticated(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "u1", Username: "alice", Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "GET", "/api/auth/me", tok, "")

	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp domain.User
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Username != "alice" {
		t.Errorf("username: got %s", resp.Username)
	}
}

func TestAuthMe_Unauthenticated_Returns401(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "GET", "/api/auth/me", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthListUsers(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	us.users["u1"] = &domain.User{ID: "u1", Username: "alice", Active: true, CreatedAt: &now}
	us.users["u2"] = &domain.User{ID: "u2", Username: "bob", Active: true, CreatedAt: &now}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "GET", "/api/auth/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list users: got %d", w.Code)
	}
	var users []*domain.User
	json.NewDecoder(w.Body).Decode(&users)
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestAuthCreateUser_Success(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	r := setupAuthRouter(t, us, ts)

	w := doRequest(r, "POST", "/api/auth/users",
		`{"username":"newuser","email":"new@example.com","password":"secure123","role":"operator"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: got %d, body: %s", w.Code, w.Body.String())
	}
	var user domain.User
	json.NewDecoder(w.Body).Decode(&user)
	if user.Username != "newuser" {
		t.Errorf("username: got %s", user.Username)
	}
}

func TestAuthCreateUser_StoreFails_Returns400(t *testing.T) {
	us := newMockUserStore()
	us.failSave = true
	r := setupAuthRouter(t, us, newMockTokenStore())

	w := doRequest(r, "POST", "/api/auth/users",
		`{"username":"fail","password":"pass","role":"viewer"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAuthDeleteUser(t *testing.T) {
	us := newMockUserStore()
	now := time.Now()
	us.users["u1"] = &domain.User{ID: "u1", Username: "todelete", Active: true, CreatedAt: &now}

	r := setupAuthRouter(t, us, newMockTokenStore())
	w := doRequest(r, "DELETE", "/api/auth/users/u1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete user: got %d", w.Code)
	}
	if us.users["u1"] != nil {
		t.Error("user should be deleted from store")
	}
}

func TestAuthListTokens_Authenticated(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "u1", Username: "alice", Active: true, CreatedAt: &now}
	us.users["u1"] = user
	ts.tokens["hash1"] = &domain.AccessToken{
		TokenHash: "hash1", UserID: "u1", Name: "ci", CreatedAt: &now,
	}

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "GET", "/api/auth/tokens", tok, "")

	if w.Code != http.StatusOK {
		t.Fatalf("list tokens: got %d, body: %s", w.Code, w.Body.String())
	}
	var tokens []*domain.AccessToken
	json.NewDecoder(w.Body).Decode(&tokens)
	if len(tokens) != 1 {
		t.Errorf("expected 1 token, got %d", len(tokens))
	}
}

func TestAuthListTokens_Unauthenticated_Returns401(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "GET", "/api/auth/tokens", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthCreateToken_Success(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "u1", Username: "alice", Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "POST", "/api/auth/tokens", tok, `{"name":"ci-token"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("create token: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == nil {
		t.Error("token should be in response")
	}
	if resp["name"] != "ci-token" {
		t.Errorf("name: got %v", resp["name"])
	}
}

func TestAuthCreateToken_Unauthenticated_Returns401(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "POST", "/api/auth/tokens", `{"name":"test"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthCreateToken_StoreFails_Returns500(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	ts.failSave = true
	now := time.Now()
	user := &domain.User{ID: "u1", Username: "alice", Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "POST", "/api/auth/tokens", tok, `{"name":"fail"}`)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestAuthRevokeToken(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	ts.tokens["abc123"] = &domain.AccessToken{
		TokenHash: "abc123", UserID: "u1", Name: "old", CreatedAt: &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "DELETE", "/api/auth/tokens/abc123", "")
	if w.Code != http.StatusOK {
		t.Fatalf("revoke token: got %d", w.Code)
	}
	if ts.tokens["abc123"] != nil {
		t.Error("token should be revoked")
	}
}
