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
	userKey           contextKey = "auth_user"
	tokenKey          contextKey = "auth_token"
	authFailureKey    contextKey = "auth_failure"
	contentTypeJSON              = "application/json"
	headerContentType            = "Content-Type"
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
// unauthenticated (so public routes such as login still work with a stale
// cookie) and RequireAuth then 401s with the token_expired code.
func ExtractAuth(lookup TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if raw := extractRawToken(r); raw != "" {
				ctx = authenticate(ctx, lookup, HashToken(raw))
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticate resolves a token hash to a user and returns the enriched
// context. An expired token leaves a failure marker instead of a user.
func authenticate(ctx context.Context, lookup TokenLookup, hash string) context.Context {
	token, _ := lookup.FindByTokenHash(ctx, hash)
	if token == nil {
		return ctx
	}
	now := time.Now()
	if TokenExpiredAt(token, now) {
		return context.WithValue(ctx, authFailureKey, ErrCodeTokenExpired)
	}
	user, _ := lookup.FindUserByID(ctx, token.UserID)
	if user == nil || !user.Active {
		return ctx
	}
	recordLastUsed(ctx, lookup, token, now)
	ctx = context.WithValue(ctx, userKey, user)
	return context.WithValue(ctx, tokenKey, token)
}

// recordLastUsed persists last_used at most once per throttle window per
// token. Best-effort: a failed write must never fail the request.
func recordLastUsed(ctx context.Context, lookup TokenLookup, token *domain.AccessToken, now time.Time) {
	recorder, ok := lookup.(LastUsedRecorder)
	if !ok || !lastUsedStale(token, now) {
		return
	}
	_ = recorder.TouchTokenLastUsed(ctx, token.TokenHash, now)
}

// authFailure returns the machine-readable reason ExtractAuth refused the
// presented token, or "" when no token was presented or it was unknown.
func authFailure(ctx context.Context) string {
	code, _ := ctx.Value(authFailureKey).(string)
	return code
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
			writeUnauthorized(w, authFailure(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeUnauthorized emits the 401 body. An expired token carries a distinct
// code so clients can prompt for rotation instead of a generic re-login.
func writeUnauthorized(w http.ResponseWriter, code string) {
	body := map[string]string{"error": "authentication required"}
	if code == ErrCodeTokenExpired {
		body["error"] = "token expired"
		body["code"] = code
	}
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(body)
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
