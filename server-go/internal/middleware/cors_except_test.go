package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A project's public functions answer CORS from the project's own allowlist,
// so the Studio-only allowlist must not answer for them first (EXC-518).
func TestExceptPathPrefix_LeavesTheExemptPathToTheHandler(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})
	handler := ExceptPathPrefix("/functions/v1/", CORS([]string{testOrigin}))(next)

	req := httptest.NewRequest(http.MethodOptions, "/functions/v1/proj_a/hello", nil)
	req.Header.Set("Origin", testEvilOrigin)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if !reached || rr.Code != http.StatusTeapot || rr.Header().Get(corsOriginHeader) != "" {
		t.Fatalf("exempt path: reached=%v code=%d ACAO=%q", reached, rr.Code, rr.Header().Get(corsOriginHeader))
	}

	reached = false
	req = httptest.NewRequest(http.MethodOptions, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if reached || rr.Code != http.StatusNoContent || rr.Header().Get(corsOriginHeader) != testOrigin {
		t.Fatalf("other paths keep the wrapped middleware: reached=%v code=%d", reached, rr.Code)
	}

	// A look-alike prefix is not exempt.
	reached = false
	req = httptest.NewRequest(http.MethodOptions, "/functions/v1x/a", nil)
	req.Header.Set("Origin", testEvilOrigin)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if reached {
		t.Fatal("/functions/v1x must not be exempt")
	}
}
