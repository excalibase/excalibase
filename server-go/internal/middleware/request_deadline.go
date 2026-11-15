package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// RequestDeadline gives every ordinary request a deadline. Handlers pass the
// request context to the platform database, so a connection that never frees
// up costs a caller at most this long instead of the minutes it used to.
// Streams, websockets, follows, deploy waits, MCP, object storage and site
// deploys are long by design and keep the connection's own lifetime.
func RequestDeadline(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isLongLived(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), limit)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isLongLived(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	path := r.URL.Path
	switch {
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"),
		strings.HasPrefix(path, "/storage/v1/"), strings.HasPrefix(path, "/functions/v1/"):
		return true
	case r.URL.Query().Get("wait") != "" || r.URL.Query().Get("follow") != "":
		return true
	}
	for _, segment := range []string{"/deploy", "/deploys", "/upload", "/import", "/restore", "/logs", "/backups"} {
		if strings.Contains(path, segment) {
			return true
		}
	}
	return false
}
