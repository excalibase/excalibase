package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testRateLimitKey = "X-Test-Key"

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// constKey returns a fixed key — used to simulate "many requests from same
// caller" without setting up a real auth context.
func constKey(k string) KeyFunc {
	return func(r *http.Request) string { return k }
}

// distinctKey hands out a unique key per request — used to verify that
// separate callers don't share a bucket.
func distinctKey(seq *int) KeyFunc {
	return func(r *http.Request) string {
		*seq++
		return string(rune('a' + *seq))
	}
}

func TestRateLimit_AllowsUpToBurst(t *testing.T) {
	mw := RateLimit(constKey("k"), 3, time.Minute)
	h := mw(okHandler())

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 200 {
			t.Fatalf("request %d should be allowed (within burst), got %d", i+1, w.Code)
		}
	}
}

func TestRateLimit_RejectsAfterBurst(t *testing.T) {
	mw := RateLimit(constKey("k"), 2, time.Minute)
	h := mw(okHandler())

	// Burn the burst.
	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request should be 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429")
	}
}

func TestRateLimit_DistinctKeysDontShareBucket(t *testing.T) {
	mw := RateLimit(func(r *http.Request) string {
		return r.Header.Get(testRateLimitKey)
	}, 1, time.Minute)
	h := mw(okHandler())

	for _, key := range []string{"alice", "bob", "carol"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set(testRateLimitKey, key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("first request from %s should be allowed (separate bucket), got %d", key, w.Code)
		}
	}
	// A second request from alice should now be limited (1/min, burst=1)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(testRateLimitKey, "alice")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second request from same key should be 429, got %d", w.Code)
	}
}

func TestRateLimit_EmptyKeyPassesThrough(t *testing.T) {
	mw := RateLimit(constKey(""), 1, time.Minute)
	h := mw(okHandler())

	// Even past "burst" the empty-key path is unrestricted.
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 200 {
			t.Fatalf("request %d with empty key should pass through, got %d", i+1, w.Code)
		}
	}
}

func TestRateLimit_RefillsOverTime(t *testing.T) {
	// 10 burst, 10 per 100ms (so refillRate = 100/sec → ~1 token every 10ms)
	mw := RateLimit(constKey("k"), 10, 100*time.Millisecond)
	h := mw(okHandler())
	for i := 0; i < 10; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}
	// Bucket empty.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when bucket empty, got %d", w.Code)
	}

	// Wait for >= 1 token to refill.
	time.Sleep(20 * time.Millisecond)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Errorf("after refill window, request should pass; got %d", w.Code)
	}
}

func TestPerIP_StripsPort(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:54321"
	if got := PerIP(r); got != "192.0.2.1" {
		t.Errorf("expected 192.0.2.1, got %q", got)
	}
}

func TestPerIP_HonoursForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := PerIP(r); got != "203.0.113.5" {
		t.Errorf("expected 203.0.113.5, got %q", got)
	}
}
