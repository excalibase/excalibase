package middleware

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/metrics"
)

const poolGuardPoll = 25 * time.Millisecond

// PoolGuard answers 503 when every platform-database connection is in use and
// stays so for the wait budget. database/sql parks callers on an exhausted
// pool for as long as their context lives, which turned a connection leak
// into minutes-long hangs; a fast, retryable refusal keeps the control plane
// answering and tells callers the request was not judged. Probes and metrics
// pass untouched: they are how the outage is noticed.
func PoolGuard(stats func() sql.DBStats, wait time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" || !poolExhausted(stats) {
				next.ServeHTTP(w, r)
				return
			}
			deadline := time.NewTimer(wait)
			defer deadline.Stop()
			tick := time.NewTicker(poolGuardPoll)
			defer tick.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-deadline.C:
					metrics.CountPoolRejected()
					w.Header().Set("Retry-After", "2")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"service temporarily unavailable","code":"unavailable"}`))
					return
				case <-tick.C:
					if !poolExhausted(stats) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
		})
	}
}

func poolExhausted(stats func() sql.DBStats) bool {
	s := stats()
	return s.MaxOpenConnections > 0 && s.InUse >= s.MaxOpenConnections
}
