package handler

import (
	"context"
	"encoding/json"
	"errors"
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

const (
	routeUsers      = "/users"
	routeTokens     = "/tokens"
	routeLogin      = "/api/auth/login"
	expect401Fmt    = "expected 401, got %d"
	routeAuthUsers  = "/api/auth/users"
	routeAuthTokens = "/api/auth/tokens"
)

// --- in-memory mock stores for auth tests ---

type mockUserStore struct {
	users      map[string]*domain.User
	failSave   bool
	failList   bool
	failDelete bool
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
	if s.failList {
		return nil, errors.New("db error")
	}
	list := make([]*domain.User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	return list, nil
}

func (s *mockUserStore) DeleteUser(_ context.Context, id string) error {
	if s.failDelete {
		return errors.New("db error")
	}
	delete(s.users, id)
	return nil
}

func (s *mockUserStore) UpdateUserPassword(_ context.Context, username, hash string) error {
	for _, u := range s.users {
		if u.Username == username {
			u.PasswordHash = hash
			return nil
		}
	}
	return nil
}

type mockTokenStore struct {
	tokens     map[string]*domain.AccessToken
	failSave   bool
	failList   bool
	failDelete bool
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
	if s.failList {
		return nil, errors.New("db error")
	}
	var result []*domain.AccessToken
	for _, tok := range s.tokens {
		if tok.UserID == userID {
			result = append(result, tok)
		}
	}
	return result, nil
}

func (s *mockTokenStore) DeleteToken(_ context.Context, tokenHash string) error {
	if s.failDelete {
		return errors.New("db error")
	}
	delete(s.tokens, tokenHash)
	return nil
}

func (s *mockTokenStore) UpdateTokenExpiry(_ context.Context, tokenHash string, expiresAt *time.Time) error {
	if tok, ok := s.tokens[tokenHash]; ok {
		tok.ExpiresAt = expiresAt
	}
	return nil
}

func (s *mockTokenStore) TouchTokenLastUsed(_ context.Context, tokenHash string, at time.Time) error {
	if tok, ok := s.tokens[tokenHash]; ok {
		tok.LastUsed = &at
	}
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
		r.Post("/logout", h.Logout)
		r.Get("/me", h.Me)
		r.Get(routeUsers, h.ListUsers)
		r.Post(routeUsers, h.CreateUser)
		r.Delete("/users/{userId}", h.DeleteUser)
		r.Get(routeTokens, h.ListTokens)
		r.Post(routeTokens, h.CreateToken)
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
	rawToken = testutil.FixtureToken("inject")
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
		r.Post("/logout", h.Logout)
		r.Get("/me", h.Me)
		r.Get(routeUsers, h.ListUsers)
		r.Post(routeUsers, h.CreateUser)
		r.Delete("/users/{userId}", h.DeleteUser)
		r.Get(routeTokens, h.ListTokens)
		r.Post(routeTokens, h.CreateToken)
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

	aliceUser := testutil.FixtureToken("alice")
	alicePwd := testutil.FixturePassword("alice-login")
	hash, _ := auth.HashPassword(alicePwd)
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID:           "u1",
		Username:     aliceUser,
		PasswordHash: hash,
		Active:       true,
		CreatedAt:    &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", routeLogin, fmt.Sprintf(`{"username":%q,"password":%q}`, aliceUser, alicePwd))

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

	w := doRequest(r, "POST", routeLogin, `{"username":"nobody","password":"not-a-real-password-xyz"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(expect401Fmt, w.Code)
	}
}

func TestAuthLogin_WrongPassword_Returns401(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()

	correctPwd := testutil.FixturePassword("bob-correct")
	hash, _ := auth.HashPassword(correctPwd)
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID: "u1", Username: "bob", PasswordHash: hash, Active: true, CreatedAt: &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", routeLogin, fmt.Sprintf(`{"username":"bob","password":%q}`, testutil.FixturePassword("bob-wrong")))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(expect401Fmt, w.Code)
	}
}

func TestAuthLogin_InvalidJSON_Returns400(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "POST", routeLogin, "not json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAuthLogin_TokenCreationFails_Returns500(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	ts.failSave = true

	carolUser := testutil.FixtureToken("carol")
	carolPwd := testutil.FixturePassword("carol-login")
	hash, _ := auth.HashPassword(carolPwd)
	now := time.Now()
	us.users["u1"] = &domain.User{
		ID: "u1", Username: carolUser, PasswordHash: hash, Active: true, CreatedAt: &now,
	}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "POST", routeLogin, fmt.Sprintf(`{"username":%q,"password":%q}`, carolUser, carolPwd))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestAuthMe_Authenticated(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	aliceUser := testutil.FixtureToken("alice")
	user := &domain.User{ID: "u1", Username: aliceUser, Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "GET", "/api/auth/me", tok, "")

	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp domain.User
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Username != aliceUser {
		t.Errorf("username: got %s", resp.Username)
	}
}

func TestAuthMe_Unauthenticated_Returns401(t *testing.T) {
	r := setupAuthRouter(t, newMockUserStore(), newMockTokenStore())
	w := doRequest(r, "GET", "/api/auth/me", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(expect401Fmt, w.Code)
	}
}

func TestAuthListUsers(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	us.users["u1"] = &domain.User{ID: "u1", Username: testutil.FixtureToken("alice"), Active: true, CreatedAt: &now}
	us.users["u2"] = &domain.User{ID: "u2", Username: testutil.FixtureToken("bob"), Active: true, CreatedAt: &now}

	r := setupAuthRouter(t, us, ts)
	w := doRequest(r, "GET", routeAuthUsers, "")
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

	w := doRequest(r, "POST", routeAuthUsers,
		fmt.Sprintf(`{"username":"newuser","email":"new@example.com","password":%q,"role":"platform_operator"}`, testutil.FixturePassword("new-user")))
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

	w := doRequest(r, "POST", routeAuthUsers,
		fmt.Sprintf(`{"username":"fail","email":"fail@example.com","password":%q,"role":"platform_viewer"}`, testutil.FixturePassword("fail-user")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAuthDeleteUser(t *testing.T) {
	us := newMockUserStore()
	now := time.Now()
	us.users["u1"] = &domain.User{ID: "u1", Username: testutil.FixtureToken("todelete"), Active: true, CreatedAt: &now}

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
	user := &domain.User{ID: "u1", Username: testutil.FixtureToken("alice"), Active: true, CreatedAt: &now}
	us.users["u1"] = user
	ciHash := testutil.FixtureToken("ci-token")
	ts.tokens[ciHash] = &domain.AccessToken{
		TokenHash: ciHash, UserID: "u1", Name: "ci", CreatedAt: &now,
	}

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "GET", routeAuthTokens, tok, "")

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
	w := doRequest(r, "GET", routeAuthTokens, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(expect401Fmt, w.Code)
	}
}

func TestAuthCreateToken_Success(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "u1", Username: testutil.FixtureToken("alice"), Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "POST", routeAuthTokens, tok, `{"name":"ci-token"}`)

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
	w := doRequest(r, "POST", routeAuthTokens, `{"name":"test"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(expect401Fmt, w.Code)
	}
}

func TestAuthCreateToken_StoreFails_Returns500(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	ts.failSave = true
	now := time.Now()
	user := &domain.User{ID: "u1", Username: testutil.FixtureToken("alice"), Active: true, CreatedAt: &now}
	us.users["u1"] = user

	r, tok := setupAuthRouterWithUser(t, us, ts, user)
	w := doAuthRequest(r, "POST", routeAuthTokens, tok, `{"name":"fail"}`)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestAuthRevokeToken(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	owner := &domain.User{ID: "u1", Username: testutil.FixtureToken("owner"), Active: true, Role: "user", CreatedAt: &now}
	us.users["u1"] = owner
	// Token to revoke — owned by u1.
	revokeHash := testutil.FixtureToken("revoke-hash")
	ts.tokens[revokeHash] = &domain.AccessToken{
		TokenHash: revokeHash, UserID: "u1", Name: "old", CreatedAt: &now,
	}

	r, tok := setupAuthRouterWithUser(t, us, ts, owner)
	w := doAuthRequest(r, "DELETE", "/api/auth/tokens/"+revokeHash, tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("revoke own token: got %d, body=%s", w.Code, w.Body.String())
	}
	if ts.tokens[revokeHash] != nil {
		t.Error("token should be revoked")
	}
}

// Login must set an httpOnly session cookie so the studio can authenticate
// without exposing the PAT to JS. Returns the token in the body too for
// SDK/curl callers, but browsers should rely on the cookie.
func TestAuthLogin_SetsSessionCookie(t *testing.T) {
	us := newMockUserStore()
	alicePwd2 := testutil.FixturePassword("alice-cookie")
	hash, _ := auth.HashPassword(alicePwd2)
	now := time.Now()
	aliceUser2 := testutil.FixtureToken("alice")
	us.users["u1"] = &domain.User{
		ID: "u1", Username: aliceUser2, PasswordHash: hash, Active: true, CreatedAt: &now,
	}
	ts := newMockTokenStore()
	r := setupAuthRouter(t, us, ts)

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, aliceUser2, alicePwd2)
	w := doRequest(r, "POST", routeLogin, body)
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d, body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			session = c
			break
		}
	}
	if session == nil {
		t.Fatalf("expected session cookie %q in response, got %v", auth.SessionCookieName, cookies)
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite: got %v, want Strict", session.SameSite)
	}
	if session.Path != "/" {
		t.Errorf("session cookie Path: got %q, want /", session.Path)
	}
	if session.Expires.IsZero() {
		t.Error("session cookie must have an expiry (12h TTL)")
	}
	// Stored token should carry session scope + future expiry.
	var stored *domain.AccessToken
	for _, tk := range ts.tokens {
		stored = tk
		break
	}
	if stored == nil {
		t.Fatal("expected token persisted on login")
	}
	if stored.Scopes != "session" {
		t.Errorf("login token scopes: got %q, want session", stored.Scopes)
	}
	if stored.ExpiresAt == nil || time.Until(*stored.ExpiresAt) > 13*time.Hour {
		t.Errorf("login token expiry should be ~12h, got %+v", stored.ExpiresAt)
	}
}

func TestAuthLogout_ClearsCookieAndRevokesToken(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "u1", Username: testutil.FixtureToken("alice"), Active: true, Role: "user", CreatedAt: &now}
	us.users["u1"] = user
	// Pre-populate a session token for this user (mimics post-login state)
	r, tok := setupAuthRouterWithUser(t, us, ts, user)

	w := doAuthRequest(r, "POST", "/api/auth/logout", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("logout: got %d body=%s", w.Code, w.Body.String())
	}
	// Token must be deleted from the store.
	if len(ts.tokens) != 0 {
		t.Errorf("token store should be empty after logout, got %d", len(ts.tokens))
	}
	// Response must clear the cookie (Max-Age <= 0 or Expires in the past).
	cookies := w.Result().Cookies()
	var clearing *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			clearing = c
			break
		}
	}
	if clearing == nil {
		t.Fatal("logout must emit a session cookie clear")
	}
	if clearing.Value != "" || (clearing.MaxAge >= 0 && !clearing.Expires.Before(time.Now())) {
		t.Errorf("session cookie should be cleared, got value=%q maxAge=%d expires=%v",
			clearing.Value, clearing.MaxAge, clearing.Expires)
	}
}

func TestAuthRevokeToken_RejectsForeign(t *testing.T) {
	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	callerID := testutil.FixtureToken("u-caller")
	otherID := testutil.FixtureToken("u-other")
	caller := &domain.User{ID: callerID, Username: testutil.FixtureToken("caller"), Active: true, Role: "user", CreatedAt: &now}
	us.users[callerID] = caller
	// Foreign token belongs to a different user.
	foreignHash := testutil.FixtureToken("foreign-tok")
	ts.tokens[foreignHash] = &domain.AccessToken{
		TokenHash: foreignHash, UserID: otherID, Name: "foreign", CreatedAt: &now,
	}

	r, tok := setupAuthRouterWithUser(t, us, ts, caller)
	w := doAuthRequest(r, "DELETE", "/api/auth/tokens/"+foreignHash, tok, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 revoking foreign token, got %d body=%s", w.Code, w.Body.String())
	}
	if ts.tokens[foreignHash] == nil {
		t.Error("foreign token must NOT be deleted on 403")
	}
}
