package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type contextKey string

const userKey contextKey = "auth_user"

// TokenLookup abstracts token + user lookup for the middleware.
type TokenLookup interface {
	FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error)
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
}

// GetUser returns the authenticated user from context, or nil.
func GetUser(ctx context.Context) *domain.User {
	u, _ := ctx.Value(userKey).(*domain.User)
	return u
}

// ExtractAuth reads Bearer token, hashes it, looks up user from DB.
func ExtractAuth(lookup TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				raw := strings.TrimPrefix(authHeader, "Bearer ")
				hash := HashToken(raw)

				token, _ := lookup.FindByTokenHash(ctx, hash)
				if token != nil {
					user, _ := lookup.FindUserByID(ctx, token.UserID)
					if user != nil && user.Active {
						ctx = context.WithValue(ctx, userKey, user)
					}
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAuth rejects requests without a valid authenticated user.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUser(r.Context()) == nil {
			w.Header().Set("Content-Type", "application/json")
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
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "insufficient permissions"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
