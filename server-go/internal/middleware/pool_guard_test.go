package middleware

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func serveThroughGuard(stats func() sql.DBStats, wait time.Duration, path string) (*httptest.ResponseRecorder, bool) {
	reached := false
	h := PoolGuard(stats, wait)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec, reached
}

func TestPoolGuard_PassesWhenAConnectionIsFree(t *testing.T) {
	stats := func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 5, InUse: 4} }
	if rec, reached := serveThroughGuard(stats, time.Second, "/api/x"); !reached || rec.Code != http.StatusOK {
		t.Fatalf("code=%d reached=%v", rec.Code, reached)
	}
}

// A pool pinned at its maximum answers 503 within the wait budget rather
// than parking the caller until something upstream gives up.
func TestPoolGuard_ExhaustedPoolAnswers503Fast(t *testing.T) {
	stats := func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 5, InUse: 5} }
	start := time.Now()
	rec, reached := serveThroughGuard(stats, 100*time.Millisecond, "/api/x")
	if reached || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d reached=%v, want 503 without reaching the handler", rec.Code, reached)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 must carry Retry-After")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v, must fail within the wait budget", took)
	}
}

// A burst that frees a connection within the budget is served, not refused.
func TestPoolGuard_WaitsBrieflyForABurstToDrain(t *testing.T) {
	var polls atomic.Int32
	stats := func() sql.DBStats {
		if polls.Add(1) < 3 {
			return sql.DBStats{MaxOpenConnections: 5, InUse: 5}
		}
		return sql.DBStats{MaxOpenConnections: 5, InUse: 4}
	}
	if rec, reached := serveThroughGuard(stats, time.Second, "/api/x"); !reached || rec.Code != http.StatusOK {
		t.Fatalf("code=%d reached=%v", rec.Code, reached)
	}
}

// Liveness and metrics are how the outage is noticed; they never wait on the pool.
func TestPoolGuard_NeverGatesProbesOrMetrics(t *testing.T) {
	stats := func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 5, InUse: 5} }
	for _, path := range []string{"/healthz", "/metrics"} {
		if _, reached := serveThroughGuard(stats, 50*time.Millisecond, path); !reached {
			t.Errorf("%s was gated", path)
		}
	}
}

func TestPoolGuard_UnlimitedPoolIsNeverGated(t *testing.T) {
	stats := func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 0, InUse: 50} }
	if _, reached := serveThroughGuard(stats, 50*time.Millisecond, "/api/x"); !reached {
		t.Fatal("an unbounded pool cannot be exhausted")
	}
}
