package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const testTokenBearer = "Bearer "

// stubLookup is a minimal TokenLookup double for middleware tests.
type stubLookup struct {
	token *domain.AccessToken
	user  *domain.User
}

func (s *stubLookup) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	if s.token != nil && s.token.TokenHash == hash {
		return s.token, nil
	}
	return nil, nil
}
func (s *stubLookup) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	if s.user != nil && s.user.ID == id {
		return s.user, nil
	}
	return nil, nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestExtractAuth_ValidHeaderAuthenticates(t *testing.T) {
	rawTok := testutil.FixtureToken("header-valid")
	user := &domain.User{ID: "u1", Active: true, Role: "user"}
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(rawTok), UserID: "u1"},
		user:  user,
	}
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r.Context()); got == nil || got.ID != "u1" {
			t.Errorf("expected user u1, got %+v", got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+rawTok)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestExtractAuth_ValidCookieAuthenticates(t *testing.T) {
	sessTok := testutil.FixtureToken("session-cookie")
	user := &domain.User{ID: "u1", Active: true, Role: "user"}
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(sessTok), UserID: "u1"},
		user:  user,
	}
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r.Context()); got == nil || got.ID != "u1" {
			t.Errorf("cookie auth failed: %+v", got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessTok})
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestExtractAuth_HeaderWinsOverCookie(t *testing.T) {
	hdrTok := testutil.FixtureToken("header-wins")
	cookTok := testutil.FixtureToken("cookie-loses")
	headerUID := testutil.FixtureToken("u-header")
	headerUser := &domain.User{ID: headerUID, Active: true, Role: "user"}
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(hdrTok), UserID: headerUID},
		user:  headerUser,
	}
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r.Context()); got == nil || got.ID != headerUID {
			t.Errorf("header should win, got %+v", got)
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+hdrTok)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookTok})
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestExtractAuth_ExpiredTokenRejected(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	expiredTok := testutil.FixtureToken("expired-tok")
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(expiredTok), UserID: "u1", ExpiresAt: &expired},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}
	calls := 0
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := GetUser(r.Context()); got != nil {
			t.Errorf("expired token must NOT authenticate, got user %+v", got)
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+expiredTok)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
}

func TestExtractAuth_FutureExpiryAuthenticates(t *testing.T) {
	future := time.Now().Add(time.Hour)
	freshTok := testutil.FixtureToken("future-expiry")
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(freshTok), UserID: "u1", ExpiresAt: &future},
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r.Context()); got == nil {
			t.Error("non-expired token must authenticate")
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+freshTok)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestExtractAuth_NilExpiryNeverExpires(t *testing.T) {
	foreverTok := testutil.FixtureToken("nil-expiry")
	lookup := &stubLookup{
		token: &domain.AccessToken{TokenHash: HashToken(foreverTok), UserID: "u1"}, // ExpiresAt nil
		user:  &domain.User{ID: "u1", Active: true, Role: "user"},
	}
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r.Context()); got == nil {
			t.Error("nil-expiry token must authenticate (long-lived CI tokens)")
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", testTokenBearer+foreverTok)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestRequireScope_AllowsMatchingScope(t *testing.T) {
	tok := &domain.AccessToken{TokenHash: "h", UserID: "u1", Scopes: "session,read"}
	mw := RequireScope("read")
	h := mw(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), tokenKey, tok))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequireScope_RejectsMissingScope(t *testing.T) {
	tok := &domain.AccessToken{TokenHash: "h", UserID: "u1", Scopes: "read"} // no admin
	mw := RequireScope("admin")
	h := mw(okHandler())

	req := httptest.NewRequest("DELETE", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), tokenKey, tok))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestRequireScope_LegacyTokenWithEmptyScopesAllowed(t *testing.T) {
	tok := &domain.AccessToken{TokenHash: "h", UserID: "u1", Scopes: ""} // legacy
	mw := RequireScope("admin")
	h := mw(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), tokenKey, tok))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("legacy empty-scope token should pass any scope check, got %d", w.Code)
	}
}

func TestRequireScope_NoTokenIn401Path(t *testing.T) {
	mw := RequireScope("read")
	h := mw(okHandler())

	req := httptest.NewRequest("GET", "/", nil) // no token in context
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("missing token must be rejected, got %d", w.Code)
	}
}

// TestRequireAuth_EnforcesTokenScopeForMethod pins the central scope gate:
// the token's scopes decide which HTTP methods it may use on EVERY
// authenticated route, not only the project-scoped ones.
func TestRequireAuth_EnforcesTokenScopeForMethod(t *testing.T) {
	cases := []struct {
		name   string
		scopes string
		method string
		want   int
	}{
		{"read PAT reads", ScopeRead, http.MethodGet, http.StatusOK},
		{"read PAT writes", ScopeRead, http.MethodPost, http.StatusForbidden},
		{"read PAT updates", ScopeRead, http.MethodPut, http.StatusForbidden},
		{"read PAT deletes", ScopeRead, http.MethodDelete, http.StatusForbidden},
		{"write PAT writes", "read,write", http.MethodPost, http.StatusOK},
		{"admin PAT writes", ScopeAdmin, http.MethodPost, http.StatusOK},
		{"session writes", ScopeSession, http.MethodDelete, http.StatusOK},
		{"legacy all-purpose PAT writes", "", http.MethodPost, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := SetUser(context.Background(), &domain.User{ID: "u1", Role: "platform_admin", Active: true})
			ctx = SetToken(ctx, &domain.AccessToken{TokenHash: "h", UserID: "u1", Scopes: tc.scopes})
			req := httptest.NewRequest(tc.method, "/api/parameter-groups/", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			RequireAuth(okHandler()).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s %s with scopes %q: got %d, want %d", tc.method, req.URL.Path, tc.scopes, w.Code, tc.want)
			}
		})
	}
}
