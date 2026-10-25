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

// RequireInternalListener answers a request that came through the edge as an
// unmounted route would.
func RequireInternalListener(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internal, _ := r.Context().Value(internalListenerKey{}).(bool); !internal {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
