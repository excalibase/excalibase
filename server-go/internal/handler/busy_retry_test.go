package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// answeredAfterBusy repeats a request until it gets an answer other than 503,
// the way a client honours Retry-After. A 503 must carry Retry-After; the test
// waits a short fraction of it, not the full seconds.
func answeredAfterBusy(t *testing.T, do func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		w := do()
		if w.Code != http.StatusServiceUnavailable {
			return w
		}
		if seconds, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || seconds < 1 {
			t.Fatalf("503 without a usable Retry-After: %q", w.Header().Get("Retry-After"))
		}
		if time.Now().After(deadline) {
			t.Fatal("still busy after 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
