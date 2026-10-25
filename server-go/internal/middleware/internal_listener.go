package middleware

import (
	"context"
	"net/http"
)

type internalListenerKey struct{}

// MarkInternalListener tags every request that arrived on the in-cluster
// listener, which the edge never forwards to.
func MarkInternalListener(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), internalListenerKey{}, true)))
	})
}

// RequireInternalListener refuses a request that came through the edge.
func RequireInternalListener(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internal, _ := r.Context().Value(internalListenerKey{}).(bool); !internal {
			http.Error(w, "this route is served to in-cluster callers only", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}
