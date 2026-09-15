package middleware

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Activity sources recorded on project_activity.last_seen_source. Aliases of
// the domain vocabulary so the classifier and its callers share one closed
// set of values.
const (
	SourceAPI            = domain.ActivitySourceAPI
	SourcePolicyFetch    = domain.ActivitySourcePolicyFetch
	SourceInfo           = domain.ActivitySourceInfo
	SourceFunctions      = domain.ActivitySourceFunctions
	SourceFunctionInvoke = domain.ActivitySourceFunctionInvoke
	SourceSchema         = domain.ActivitySourceSchema
	SourceMigration      = domain.ActivitySourceMigration
	SourceBackup         = domain.ActivitySourceBackup
	SourceRealtime       = domain.ActivitySourceRealtime
	SourceStorage        = domain.ActivitySourceStorage
)

// ActivityRecorder is the sink the middleware feeds. Implemented by
// service.ActivityRecorder, which throttles writes per project.
type ActivityRecorder interface {
	Record(ctx context.Context, projectID string, source domain.ActivitySource)
}

// ProjectActivity marks a project as seen after every successful (< 400)
// request that carries a {projectId} URL param. Mount it after the auth /
// project-access guards so rejected calls never count — and so the recorder's
// throttle map only ever holds ids that resolved to a real project. A nil
// recorder makes the middleware a transparent no-op.
func ProjectActivity(rec ActivityRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if rec == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectID := chi.URLParam(r, "projectId")
			if projectID == "" {
				next.ServeHTTP(w, r)
				return
			}
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if ww.Status() < http.StatusBadRequest {
				rec.Record(r.Context(), projectID, ActivitySource(r.URL.Path))
			}
		})
	}
}

// activitySourceBySegment maps a path segment to its source. The first
// matching segment wins, so ordering only matters for paths that carry
// several of these words — none do today.
var activitySourceBySegment = []struct {
	segment string
	source  domain.ActivitySource
}{
	{"rls-policies", SourcePolicyFetch},
	{"column-policies", SourcePolicyFetch},
	{"info", SourceInfo},
	{"functions", SourceFunctions},
	{"schema", SourceSchema},
	{"migrations", SourceMigration},
	{"backup", SourceBackup},
	{"snapshot", SourceBackup},
	{"realtime", SourceRealtime},
	{"storage", SourceStorage},
}

// ActivitySource classifies a request path into one of the Source* values.
// The path is only ever compared against fixed segments; the result is
// always one of the named constants, never a slice of the input.
func ActivitySource(path string) domain.ActivitySource {
	if strings.HasPrefix(path, "/functions/v1/") || strings.HasPrefix(path, "/internal/invoke/") {
		return SourceFunctionInvoke
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for _, entry := range activitySourceBySegment {
		if slices.Contains(segments, entry.segment) {
			return entry.source
		}
	}
	return SourceAPI
}
