package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type contextKey string

const (
	userKey         contextKey = "auth_user"
	tokenKey        contextKey = "auth_token"
	contentTypeJSON            = "application/json"
	headerContentType          = "Content-Type"
)

// TokenLookup abstracts token + user lookup for the middleware.
type TokenLookup interface {
	FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error)
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
}

// SetUser returns a new context with the given user set (for testing).
func SetUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// GetUser returns the authenticated user from context, or nil.
func GetUser(ctx context.Context) *domain.User {
	u, _ := ctx.Value(userKey).(*domain.User)
	return u
}

// SessionCookieName is the cookie that holds the studio's session PAT.
// Operators invoking the API from CI/scripts use the Authorization header
// instead — both paths land on ExtractAuth.
const SessionCookieName = "excali_session"

// extractRawToken returns the raw bearer token from either the
// Authorization header (CI/scripts) or the session cookie (studio). Header
// wins when both are present so explicit overrides work.
func extractRawToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// ExtractAuth reads the bearer token (header or cookie), hashes it, and
// looks up the user. Expired tokens are rejected — the request continues
// unauthenticated, RequireAuth then 401s.
func ExtractAuth(lookup TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			raw := extractRawToken(r)
			if raw != "" {
				hash := HashToken(raw)
				token, _ := lookup.FindByTokenHash(ctx, hash)
				if token != nil && !tokenExpired(token) {
					user, _ := lookup.FindUserByID(ctx, token.UserID)
					if user != nil && user.Active {
						ctx = context.WithValue(ctx, userKey, user)
						ctx = context.WithValue(ctx, tokenKey, token)
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// tokenExpired returns true if the token has a non-nil expiry that is in
// the past. NULL ExpiresAt = never expires (long-lived CI token).
func tokenExpired(t *domain.AccessToken) bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// GetToken returns the AccessToken used to authenticate the current
// request, or nil. Useful for handlers that need to check scope.
func GetToken(ctx context.Context) *domain.AccessToken {
	t, _ := ctx.Value(tokenKey).(*domain.AccessToken)
	return t
}

// RequireScope rejects the request if the authenticating token does not
// carry the named scope. Tokens with no scopes set are treated as legacy
// "all-purpose" tokens and pass through (backwards compat — pre-existing
// PATs predate the scopes column). New tokens always carry at least one
// scope, so this branch only fires for legacy data.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := GetToken(r.Context())
			if t == nil || !TokenHasScope(t, scope) {
				w.Header().Set(headerContentType, contentTypeJSON)
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "token lacks required scope: " + scope})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TokenHasScope reports whether t carries the given scope. Empty Scopes =
// legacy "all-purpose" — passes any scope check.
func TokenHasScope(t *domain.AccessToken, scope string) bool {
	if t == nil {
		return false
	}
	if t.Scopes == "" {
		return true
	}
	for _, s := range strings.Split(t.Scopes, ",") {
		if strings.TrimSpace(s) == scope {
			return true
		}
	}
	return false
}

// RequireAuth rejects requests without a valid authenticated user.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUser(r.Context()) == nil {
			w.Header().Set(headerContentType, contentTypeJSON)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePermission checks if the authenticated user has the given permission.
func RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUser(r.Context())
			if user == nil || !HasPermission(user.Role, perm) {
				w.Header().Set(headerContentType, contentTypeJSON)
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "insufficient permissions"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
