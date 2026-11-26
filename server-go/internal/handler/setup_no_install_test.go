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

// EXC-520: the platform's own cluster role cannot create the CRDs, cluster
// roles and webhooks an operator manifest holds, so an install over HTTP could
// only fail, while a remote YAML applied cluster-wide is the risk it carried.
// Operators are installed by the chart or setup-k8s.sh; the API has no install.
func TestSetup_HasNoOperatorInstall(t *testing.T) {
	mock := k8s.NewMockClient()
	h := NewSetupHandler(service.NewOperatorSetupService(mock))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "u", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/setup", func(r chi.Router) { h.Routes(r) })

	for _, databaseType := range []string{"POSTGRESQL", "MYSQL", "REDIS"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/setup/install/"+databaseType, nil))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("install %s answered %d, want the route gone", databaseType, w.Code)
		}
	}
	for _, call := range mock.Calls {
		t.Errorf("the cluster was reached: %s", call)
	}
}
