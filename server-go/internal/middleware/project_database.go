package middleware

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

var errBodyNoDatabase = `{"error":"` + domain.ErrNoDatabase.Error() + `"}`

// RequireProjectDatabase refuses, with 409, a route that works on the
// project's database when the project was created without one (EXC-426). It
// is mounted on every such subtree so a handler never has to discover the
// missing cluster, credential or pod by failing against it. It sits behind
// RequireProjectAccess, which has already answered for unknown projects; it
// repeats that 404 rather than guessing when the row cannot be read.
func RequireProjectDatabase(instances storage.InstanceStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if instances == nil {
				http.Error(w, `{"error":"service unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			inst, err := instances.FindByProjectID(chi.URLParam(r, "projectId"))
			if err != nil {
				// An unanswered read is not "has a database".
				http.Error(w, `{"error":"service unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			if inst == nil {
				http.Error(w, errBodyProjectNotFound, http.StatusNotFound)
				return
			}
			if inst.NoDatabase {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(errBodyNoDatabase))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
