package middleware

import (
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// RequireServableProject refuses a request whose {projectId} names a project
// the platform must not serve — under teardown, or restored but not yet
// confirmed (domain.IsNotServable).
//
// It exists for the routes that carry a project id but sit outside
// RequireProjectAccess because they are anonymous: the public storage object
// path is the one that matters today. The answer is 404 rather than 409 —
// an anonymous caller is downloading a file, and a project it may not reach
// is indistinguishable from one that does not exist.
func RequireServableProject(instances storage.InstanceStore, owners ...*orgguard.Guard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No store wired means no opinion: a deployment that never
			// passed one must not lose the route entirely.
			if instances == nil {
				next.ServeHTTP(w, r)
				return
			}
			projectID := chi.URLParam(r, "projectId")
			if projectID == "" {
				next.ServeHTTP(w, r)
				return
			}
			inst, err := instances.FindByProjectID(projectID)
			if err != nil || inst == nil || domain.IsNotServable(inst.Status) {
				http.Error(w, errBodyProjectNotFound, http.StatusNotFound)
				return
			}
			// A suspended owner's project reads as absent too, so its public
			// files and end-user storage stop being served (EXC-845).
			for _, guard := range owners {
				err := guard.CheckServing(r.Context(), projectID)
				switch {
				case err == nil:
				case errors.Is(err, orgguard.ErrOrgSuspended):
					http.Error(w, errBodyProjectNotFound, http.StatusNotFound)
					return
				default:
					// Not judged: a retryable 503, never the 404 that says the project is gone.
					w.Header().Set("Retry-After", "5")
					writeSuspendedBody(w, http.StatusServiceUnavailable, errBodyUnavailable)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
