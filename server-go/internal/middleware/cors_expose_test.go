package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Studio on another origin (development) must read the mark that tells a
// relayed function answer from the platform's own 401.
func TestCORS_ExposesTheFunctionResponseMark(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, FunctionResponseHeader) {
		t.Errorf("Expose-Headers %q should name %s", got, FunctionResponseHeader)
	}
}
