package middleware

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/go-chi/chi/v5"
)

const (
	errBodySuspended   = `{"error":"` + orgguard.Message + `"}`
	errBodyStandingErr = `{"error":"the organisation's standing could not be checked; try again"}`
)

// suspendedMayWrite are the writes a member of a suspended owner's org may
// still make on a project route. Everything else that changes anything is
// refused, so a route added later is closed until someone decides otherwise
// (EXC-845). Reads stay open: the member can see what happened.
//
//   - DELETE: removing a project, app, site, function or key starts and
//     exposes nothing, and lets a member clean up.
//   - /pause: stopping compute is what the suspension wants.
var suspendedMayWrite = []struct {
	method string
	path   *regexp.Regexp
}{
	{http.MethodDelete, regexp.MustCompile(`.`)},
	{http.MethodPost, regexp.MustCompile(`/pause/?$`)},
}

// SuspendedMayWrite reports whether a write to a project route stays open to
// the members of a suspended owner's org.
func SuspendedMayWrite(method, path string) bool {
	for _, allowed := range suspendedMayWrite {
		if allowed.method == method && allowed.path.MatchString(path) {
			return true
		}
	}
	return false
}

// RefuseSuspendedWrites answers 403 to a write on a project whose org owner is
// abuse-suspended (EXC-845); a suspended account that only created a project
// in a good-standing org changes nothing here. It sits after RequireProjectAccess,
// which carries a platform admin's override: an admin may still act. The
// service-level checks of EXC-843 stay as the second line; this is the one
// place that already resolves the project, so no handler can forget it.
// A nil guard allows everything.
func RefuseSuspendedWrites(guard *orgguard.Guard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectID := chi.URLParam(r, "projectId")
			if guard == nil || projectID == "" || isSafeMethod(r.Method) || SuspendedMayWrite(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			err := guard.CheckServingUnlessAdmin(r.Context(), projectID)
			switch {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, orgguard.ErrOrgSuspended):
				writeSuspendedBody(w, http.StatusForbidden, errBodySuspended)
			default:
				w.Header().Set("Retry-After", "5")
				writeSuspendedBody(w, http.StatusServiceUnavailable, errBodyStandingErr)
			}
		})
	}
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func writeSuspendedBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
