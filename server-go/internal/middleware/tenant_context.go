package middleware

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// contextKey is an unexported type for context keys defined in this package.
// Using an unexported custom type prevents collisions with keys defined in
// other packages per context.WithValue best practice.
type contextKey string

const tenantIDKey contextKey = "tenant_id"

// TenantContext is a chi middleware that extracts the {projectId} URL param
// and attaches it to the request context as a tenant identifier. Downstream
// handlers can retrieve it via TenantIDFromContext for structured logging,
// tracing, and metrics tagging.
//
// When projectId is absent (e.g. on routes without the param), the middleware
// is a no-op and passes the request through untouched.
//
// OTEL integration: once go.opentelemetry.io/otel is a direct dependency,
// set a span attribute here for structured tracing:
//
//	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("tenant.id", projectID))
//
// OTEL packages are currently only indirect transitive dependencies via
// client-go, so importing them here would force an unwanted direct dep.
func TenantContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		if projectID != "" {
			ctx := context.WithValue(r.Context(), tenantIDKey, projectID)
			r = r.WithContext(ctx)
			log.Printf("tenant=%s method=%s path=%s", projectID, r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

// TenantIDFromContext returns the tenant (project) identifier previously
// attached by TenantContext. The second return value reports whether a tenant
// id was present; callers must check it before relying on the string.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(tenantIDKey).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
