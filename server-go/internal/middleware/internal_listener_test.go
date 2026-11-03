package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// EXC-658: a route for in-cluster callers only answers on the internal
// listener, whatever credential arrives through the edge.
func TestRequireInternalListener(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	gated := RequireInternalListener(next)

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/x", nil))
	if rec.Code != http.StatusNotFound || reached {
		t.Errorf("public listener: %d, reached %v", rec.Code, reached)
	}

	rec = httptest.NewRecorder()
	MarkInternalListener(gated).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/x", nil))
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("internal listener: %d, reached %v", rec.Code, reached)
	}
}
