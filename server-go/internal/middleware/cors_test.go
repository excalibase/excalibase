package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testOrigin          = "https://app.excalibase.io"
	testAPIPath         = "/api/test"
	corsOriginHeader    = "Access-Control-Allow-Origin"
	corsOriginFmt       = "Allow-Origin: got %q, want %q"
	testEvilOrigin      = "https://evil.com"
	testLocalhostOrigin = "http://localhost:3000"
	testAnythingOrigin  = "https://anything.com"
)

func TestCORS_AllowedOrigin(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get(corsOriginHeader); got != testOrigin {
		t.Errorf(corsOriginFmt, got, testOrigin)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials: got %q, want %q", got, "true")
	}
}

func TestCORS_BlockedOrigin(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
	req.Header.Set("Origin", testEvilOrigin)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get(corsOriginHeader); got != "" {
		t.Errorf("Allow-Origin should be empty for blocked origin, got %q", got)
	}
}

func TestCORS_NoOriginHeader(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get(corsOriginHeader); got != "" {
		t.Errorf("Allow-Origin should be empty when no Origin header, got %q", got)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestCORS_Preflight(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for preflight")
	}))

	req := httptest.NewRequest(http.MethodOptions, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("preflight status: got %d, want %d", rr.Code, http.StatusNoContent)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("preflight should include Allow-Methods")
	}
	if got := rr.Header().Get("Access-Control-Max-Age"); got != "3600" {
		t.Errorf("Max-Age: got %q, want %q", got, "3600")
	}
}

func TestCORS_PreflightBlockedOrigin(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodOptions, testAPIPath, nil)
	req.Header.Set("Origin", testEvilOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusNoContent)
	}
	if got := rr.Header().Get(corsOriginHeader); got != "" {
		t.Errorf("Allow-Origin should be empty for blocked origin, got %q", got)
	}
}

func TestCORS_MultipleOrigins(t *testing.T) {
	origins := []string{testOrigin, testLocalhostOrigin}
	handler := CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"app origin", testOrigin, testOrigin},
		{"localhost origin", testLocalhostOrigin, testLocalhostOrigin},
		{"blocked origin", testEvilOrigin, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
			req.Header.Set("Origin", tt.origin)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if got := rr.Header().Get(corsOriginHeader); got != tt.want {
				t.Errorf(corsOriginFmt, got, tt.want)
			}
		})
	}
}

func TestCORS_WildcardAllowsAll(t *testing.T) {
	handler := CORS([]string{"*"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
	req.Header.Set("Origin", testAnythingOrigin)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get(corsOriginHeader); got != testAnythingOrigin {
		t.Errorf(corsOriginFmt, got, testAnythingOrigin)
	}
}

func TestCORS_VaryHeader(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary: got %q, want %q", got, "Origin")
	}
}

func TestCORS_PreflightAllowsIfMatch(t *testing.T) {
	handler := CORS([]string{testOrigin})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, testAPIPath, nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	req.Header.Set("Access-Control-Request-Headers", "if-match")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "If-Match") {
		t.Errorf("Allow-Headers: got %q, want it to include If-Match", got)
	}
}
