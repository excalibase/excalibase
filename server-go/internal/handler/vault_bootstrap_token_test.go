package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// vaultRouterWithToken serves the vault routes to a service principal
// authenticated by a capability token carrying permissions.
func vaultRouterWithToken(t *testing.T, permissions []string) (chi.Router, *vault.Vault) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	h := NewVaultHandler(v)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user := &domain.User{ID: "svc", Username: "svc-bootstrap", Role: "platform_admin", Active: true, Kind: domain.UserKindService}
			ctx := auth.SetUser(req.Context(), user)
			ctx = auth.SetToken(ctx, &domain.AccessToken{UserID: "svc", Permissions: permissions})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/vault", h.Routes)
	return r, v
}

func vaultCall(r chi.Router, method, path, body string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w.Code
}

// The bootstrap Job initializes the vault with its svc-bootstrap capability
// token instead of an admin session (EXC-485). The token reaches init and
// nothing else in the lifecycle family.
func TestVaultInitAcceptsTheBootstrapCapability(t *testing.T) {
	r, v := vaultRouterWithToken(t, []string{"vault:init"})
	if code := vaultCall(r, http.MethodPost, "/api/vault/init", `{"shares":1,"threshold":1}`); code != http.StatusOK {
		t.Fatalf("init with vault:init: %d, want 200", code)
	}
	if !v.Initialized() {
		t.Fatal("vault must be initialized")
	}
	for _, path := range []string{"/api/vault/seal", "/api/vault/rekey", "/api/vault/unseal"} {
		if code := vaultCall(r, http.MethodPost, path, `{"shares":1,"threshold":1}`); code != http.StatusForbidden {
			t.Errorf("POST %s with only vault:init: %d, want 403", path, code)
		}
	}
	if code := vaultCall(r, http.MethodPut, "/api/vault/secrets/pki/signing/private", `{"key":"x"}`); code != http.StatusForbidden {
		t.Errorf("secret write with only vault:init: %d, want 403", code)
	}
}

func TestVaultInitRefusesAServiceTokenWithoutTheCapability(t *testing.T) {
	r, v := vaultRouterWithToken(t, []string{"vault:read:pki/signing/*"})
	if code := vaultCall(r, http.MethodPost, "/api/vault/init", `{"shares":1,"threshold":1}`); code != http.StatusForbidden {
		t.Fatalf("init without vault:init: %d, want 403", code)
	}
	if v.Initialized() {
		t.Fatal("refused init must not initialize the vault")
	}
}
