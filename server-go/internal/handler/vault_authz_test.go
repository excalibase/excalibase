package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// setupVaultRouterAs builds a vault router whose injected user carries the
// given platform role. Mirrors setupVaultRouter but lets each test pick the
// caller's role so we can prove the PermViewCredentials gate.
func setupVaultRouterAs(t *testing.T, role string) (chi.Router, *vault.Vault) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	// Init + unseal so secret routes reach the store rather than 503ing.
	if _, err := v.Init(3, 2); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	h := NewVaultHandler(v)
	// The secret read is bound to the project the path names, so the caller
	// has to have access to tenant-b for this test to be about the permission
	// gate rather than the binding.
	vaultProjectStores(h, "org-b", []string{"u-" + role}, "tenant-b")
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := &domain.User{ID: "u-" + role, Username: role, Role: role, Active: true}
			ctx := auth.SetUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Route("/api/vault", h.Routes)
	return r, v
}

// TestVaultSecrets_RequiresViewCredentials proves the authz hole is closed:
// the /api/vault/secrets/* and /secrets-list routes are gated on
// PermViewCredentials. The provisioning PAT authenticates as a platform role
// that HOLDS this permission (platform_admin / platform_operator), so the
// GraphQL engine + internal services keep working. A normal dashboard tenant
// (role "user") and a read-only platform_viewer do NOT hold it and get 403.
func TestVaultSecrets_RequiresViewCredentials(t *testing.T) {
	const credPath = "/api/vault/secrets/projects/tenant-b/credentials/excalibase_app"

	// --- Privileged roles (the PAT's roles) can read secrets ---
	for _, role := range []string{"platform_admin", "platform_operator"} {
		t.Run("allow_"+role, func(t *testing.T) {
			r, v := setupVaultRouterAs(t, role)
			// Seed a secret so a successful read returns 200 (not 404).
			if err := v.Put("projects/tenant-b/credentials/excalibase_app",
				map[string]string{"host": "db", "username": "app", "password": "p", "database": "d"}); err != nil {
				t.Fatalf("seed secret: %v", err)
			}
			req := httptest.NewRequest("GET", credPath, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("%s reading creds: got %d, want 200 (body=%s)", role, w.Code, w.Body.String())
			}
		})
	}

	// --- Unprivileged roles are rejected with 403 before touching secrets ---
	for _, role := range []string{"user", "platform_viewer"} {
		t.Run("deny_"+role, func(t *testing.T) {
			r, v := setupVaultRouterAs(t, role)
			if err := v.Put("projects/tenant-b/credentials/excalibase_app",
				map[string]string{"host": "db", "username": "app", "password": "p", "database": "d"}); err != nil {
				t.Fatalf("seed secret: %v", err)
			}

			// GET secret → 403
			req := httptest.NewRequest("GET", credPath, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s GET secret: got %d, want 403 (body=%s)", role, w.Code, w.Body.String())
			}

			// secrets-list → 403 (no enumeration of secret paths either)
			req = httptest.NewRequest("GET", "/api/vault/secrets-list", nil)
			w = httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s secrets-list: got %d, want 403", role, w.Code)
			}
		})
	}
}
