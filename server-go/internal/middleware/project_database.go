package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/routepolicy"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

var errBodyNoDatabase = `{"error":"` + domain.ErrNoDatabase.Error() + `"}`

// notForMySQL refuses a Postgres-only database route on a MySQL project.
const notForMySQL = "not available for MySQL projects"

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
			// A MySQL project reaches only the database routes the route
			// table says serve it; an unknown route is refused (ADR 0041).
			if inst.DBType == domain.MySQL && !servesMySQL(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// servesMySQL reports whether the route serves a MySQL project, and refuses
// the request with the route's reason when it does not.
func servesMySQL(w http.ResponseWriter, r *http.Request) bool {
	row, known := routepolicy.Lookup(r.Method, r.URL.Path)
	if known && row.MySQL {
		return true
	}
	refuseForMySQL(w, row.MySQLRefusal)
	return false
}

// refuseForMySQL answers 409 with the route's reason when it has one.
func refuseForMySQL(w http.ResponseWriter, reason string) {
	message := notForMySQL
	if reason != "" {
		message += ": " + reason
	}
	body, _ := json.Marshal(map[string]string{"error": message})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write(body)
}
