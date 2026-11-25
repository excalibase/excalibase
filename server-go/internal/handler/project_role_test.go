package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// callerWithProjectRole puts a resolved project access holding role on the
// request, as RequireProjectAccess does in production. An empty role leaves
// the request with no resolved access.
func callerWithProjectRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role != "" {
				access := &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}}
				r = r.WithContext(custommw.WithProjectAccess(r.Context(), access))
			}
			next.ServeHTTP(w, r)
		})
	}
}
