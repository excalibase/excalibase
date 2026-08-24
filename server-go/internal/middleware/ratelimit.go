package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// KeyFunc derives a rate-limit bucket key from a request. Returning ""
// disables rate limiting for that request (e.g. unauthenticated route's
// per-user limiter has no user yet).
type KeyFunc func(r *http.Request) string

// PerIP keys by client IP, walking X-Forwarded-For if present (only the
// first hop — anything beyond the trusted proxy is attacker-controlled).
// Use behind a trusted reverse proxy that strips and re-sets XFF.
func PerIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// PerUser keys by authenticated user ID. Returns "" if no user is in
// context — caller decides whether to allow (typically combined with
// PerIP fallback) or fail closed.
func PerUser(r *http.Request) string {
	u := auth.GetUser(r.Context())
	if u == nil {
		return ""
	}
	return "u:" + u.ID
}

// PerProjectAndUser combines projectId from URL with user ID. Used on the
// per-project data plane so a chatty user on one project doesn't choke a
// quiet user on another.
func PerProjectAndUser(r *http.Request) string {
	pid := projectIDFromURL(r)
	u := auth.GetUser(r.Context())
	uid := ""
	if u != nil {
		uid = u.ID
	}
	if pid == "" && uid == "" {
		return ""
	}
	return "p:" + pid + "/u:" + uid
}

// projectIDFromURL reads the project ID that TenantContext middleware
// already attached. Returns "" on routes where TenantContext didn't fire.
func projectIDFromURL(r *http.Request) string {
	if pid, ok := TenantIDFromContext(r.Context()); ok {
		return pid
	}
	return ""
}

// rateLimiter is a single token-bucket entry.
type rateLimiter struct {
	tokens     float64
	capacity   float64
	refillRate float64 // tokens per second
	last       time.Time
	mu         sync.Mutex
}

func (b *rateLimiter) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RateLimit returns a middleware that allows `burst` requests per
// `window` per key. Keys are derived per request via keyFn. Empty key from
// keyFn → request is passed through (no limiter applies).
//
// In-memory only — fine for single-instance deployments. For multi-instance
// you'd plug in a shared backend (Redis token bucket); the API stays the
// same.
//
// Buckets are GC'd lazily when their key hasn't been touched for >= 10×
// window — bounds memory under random-key scans (e.g. IP scans).
func RateLimit(keyFn KeyFunc, burst int, window time.Duration) func(http.Handler) http.Handler {
	if burst < 1 {
		burst = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	rate := float64(burst) / window.Seconds()
	state := &rateState{
		buckets: make(map[string]*rateLimiter),
		burst:   burst,
		rate:    rate,
		gcAfter: window * 10,
	}
	go state.gcLoop(window * 5) // periodic eviction

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			b := state.bucketFor(key)
			if !b.take() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type rateState struct {
	mu      sync.Mutex
	buckets map[string]*rateLimiter
	burst   int
	rate    float64
	gcAfter time.Duration
}

func (s *rateState) bucketFor(key string) *rateLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.buckets[key]; ok {
		return b
	}
	b := &rateLimiter{
		tokens:     float64(s.burst),
		capacity:   float64(s.burst),
		refillRate: s.rate,
		last:       time.Now(),
	}
	s.buckets[key] = b
	return b
}

// gcLoop periodically evicts buckets that haven't been touched recently.
// Bounds memory under unbounded distinct-key traffic (IP scans, random
// project IDs). Runs forever; finalised at process exit.
func (s *rateState) gcLoop(interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for now := range tick.C {
		s.mu.Lock()
		for k, b := range s.buckets {
			b.mu.Lock()
			stale := now.Sub(b.last) > s.gcAfter
			b.mu.Unlock()
			if stale {
				delete(s.buckets, k)
			}
		}
		s.mu.Unlock()
	}
}
