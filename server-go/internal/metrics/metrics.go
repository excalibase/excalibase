// Package metrics defines the custom Prometheus collectors exposed by the
// control plane on /metrics. Collectors register on the default registry via
// promauto, so promhttp.Handler() surfaces them alongside the Go runtime
// metrics without any extra wiring.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// httpRequests counts every processed HTTP request, labeled by the matched
	// chi route pattern, method and response status.
	httpRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "excalibase_http_requests_total",
			Help: "Total HTTP requests processed, labeled by route, method and status.",
		},
		[]string{"route", "method", "status"},
	)

	// provisionDuration tracks how long the database provisioning flow takes,
	// split by outcome so success and failure latencies stay distinguishable.
	provisionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "excalibase_provision_duration_seconds",
			Help:    "Duration of database provisioning operations in seconds.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // ~1s .. ~1024s
		},
		[]string{"result"},
	)

	// provisionErrors counts failed provisioning operations.
	provisionErrors = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "excalibase_provision_errors_total",
			Help: "Total number of failed database provisioning operations.",
		},
	)
)

// Middleware records one excalibase_http_requests_total sample per request,
// labeled by the matched chi route pattern, method and response status. It is
// meant to sit early in the chain so it observes the status every downstream
// handler and middleware ultimately writes.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK // handler wrote a body without an explicit header
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(status)).Inc()
	})
}

// ObserveProvision records the duration and outcome of a provisioning
// operation. Pass the error returned by the provision flow (nil on success);
// a non-nil err also increments excalibase_provision_errors_total.
func ObserveProvision(start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "error"
		provisionErrors.Inc()
	}
	provisionDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
}
