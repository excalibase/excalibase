package middleware

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

// RequestDeadline gives every ordinary request a deadline. Handlers pass the
// request context to the platform database, so a connection that never frees
// up costs a caller at most this long instead of the minutes it used to.
// Only the routes in longRoutes, which wait on long work by design, keep the
// connection's own lifetime; nothing the client sends (a query parameter, an
// Upgrade or Accept header) can lift the deadline.
func RequestDeadline(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isLongRoute(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), limit)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type longRoute struct {
	methods []string // empty: any method
	pattern *regexp.Regexp
}

func route(pattern string, methods ...string) longRoute {
	return longRoute{methods: methods, pattern: regexp.MustCompile("^" + pattern + "$")}
}

const seg = `[^/]+`

// longRoutes are the routes that stream or wait synchronously on long work:
// log follows, deploys, backups and restores, snapshot export and download,
// upgrades, project create, pause, resume and delete, and the prefixes that
// proxy or stream (MCP, object storage, functions).
var longRoutes = []longRoute{
	route(`/api/projects/`+seg+`/apps/`+seg+`/logs`, http.MethodGet),
	route(`/api/projects/`+seg+`/functions/`+seg+`/logs`, http.MethodGet),
	route(`/api/provision/`+seg+`/logs`, http.MethodGet),
	route(`/api/admin/logs`, http.MethodGet),
	route(`/api/projects/`+seg+`/apps/`+seg+`/deploy`, http.MethodGet, http.MethodPost),
	route(`/api/projects/`+seg+`/apps/`+seg+`/deploys/`+seg+`/redeploy`, http.MethodPost),
	route(`/api/projects/`+seg+`/apps/`+seg+`/pause`, http.MethodPost),
	route(`/api/projects/`+seg+`/app-templates/`+seg+`/deploy`, http.MethodPost),
	route(`/api/projects/`+seg+`/sites/`+seg+`/deploy`, http.MethodPost),
	route(`/api/projects/`+seg+`/sites/`+seg+`/deploys/`+seg+`/rollback`, http.MethodPost),
	route(`/api/provision/?`, http.MethodPost),
	route(`/api/provision/`+seg, http.MethodDelete),
	route(`/api/provision/`+seg+`/(pause|resume|upgrade)`, http.MethodPost),
	route(`/api/provision/`+seg+`/major-upgrade/?`, http.MethodPost),
	route(`/api/provision/`+seg+`/backup/(trigger|restore|copy)`, http.MethodPost),
	route(`/api/provision/`+seg+`/backups/purge`, http.MethodPost),
	route(`/api/provision/`+seg+`/snapshot/export`, http.MethodPost),
	route(`/api/provision/`+seg+`/snapshot/`+seg+`/download`, http.MethodGet),
	route(`/mcp(/.*)?`),
	route(`/storage/v1/.*`),
	route(`/internal/storage/.*`),
	route(`/functions/v1/.*`),
}

func isLongRoute(method, path string) bool {
	for _, candidate := range longRoutes {
		if !candidate.pattern.MatchString(path) {
			continue
		}
		if len(candidate.methods) == 0 {
			return true
		}
		for _, allowed := range candidate.methods {
			if allowed == method {
				return true
			}
		}
	}
	return false
}
