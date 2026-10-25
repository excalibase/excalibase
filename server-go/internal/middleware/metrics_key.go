package middleware

import (
	"net/http"
	"regexp"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// A metrics key (EXC-657) is default-deny, like a site deploy token: it may
// read its own project's Prometheus endpoint, and every other route refuses
// it. Past this gate it is its creator bound to the project, so the route's
// own project-access gate still applies.

const errMetricsKeyScope = "a metrics key may only read its own project's metrics"

var metricsKeyRoute = regexp.MustCompile(`^/api/projects/([^/]+)/metrics/?$`)

// metricsKeyMayCall reports whether a metrics key bound to projectID may make r.
func metricsKeyMayCall(r *http.Request, projectID string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	// chi routes on the escaped path; refuse any path the gate could read differently.
	if r.URL.RawPath != "" || hasDotSegment(r.URL.Path) {
		return false
	}
	match := metricsKeyRoute.FindStringSubmatch(r.URL.Path)
	return match != nil && projectID != "" && match[1] == projectID
}

// MetricsKeyGate refuses a metrics key on every route but its own. Mount it
// on the root router right after auth.ExtractAuth.
func MetricsKeyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := auth.GetToken(r.Context())
		if auth.IsMetricsKey(token) && !metricsKeyMayCall(r, token.ProjectID) {
			writeGateError(w, http.StatusForbidden, errMetricsKeyScope)
			return
		}
		next.ServeHTTP(w, r)
	})
}
