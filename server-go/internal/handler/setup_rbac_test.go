package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// TestSetup_InstallRequiresManageSetup pins SEC-H5: installing a cluster-wide
// database operator (applies a remote YAML into the cluster) must require the
// manage_setup permission — a platform_admin capability. A normal dashboard
// user or platform_operator/viewer being merely authenticated is not enough.
func TestSetup_InstallRequiresManageSetup(t *testing.T) {
	h := NewSetupHandler(service.NewOperatorSetupService(k8s.NewMockClient()))

	routerAs := func(role string) http.Handler {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := auth.SetUser(req.Context(), &domain.User{ID: "u", Role: role, Active: true})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Route("/api/setup", func(r chi.Router) { h.Routes(r) })
		return r
	}

	for _, role := range []string{"user", "platform_viewer", "platform_operator"} {
		req := httptest.NewRequest("POST", "/api/setup/install/POSTGRESQL", nil)
		w := httptest.NewRecorder()
		routerAs(role).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("role %s installing operator: got %d, want 403 (SEC-H5)", role, w.Code)
		}
	}
}
