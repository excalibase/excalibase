package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// scrape renders the default registry through promhttp and returns the body.
func scrape(t *testing.T) string {
	t.Helper()
	r := chi.NewRouter()
	r.Handle("/metrics", promhttp.Handler())
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics status: got %d, want 200", w.Code)
	}
	return w.Body.String()
}

func TestMiddlewareCountsRequests(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ping", nil))

	body := scrape(t)
	if !strings.Contains(body, "excalibase_http_requests_total") {
		t.Fatal("expected excalibase_http_requests_total in /metrics output")
	}
	if !strings.Contains(body, `route="/ping"`) || !strings.Contains(body, `method="GET"`) {
		t.Errorf("expected route/method labels in output:\n%s", body)
	}
}

func TestObserveProvisionErrorIncrements(t *testing.T) {
	before := testutil.ToFloat64(provisionErrors)
	ObserveProvision(time.Now().Add(-2*time.Second), errors.New("boom"))
	after := testutil.ToFloat64(provisionErrors)
	if after != before+1 {
		t.Errorf("provision errors: got %v, want %v", after, before+1)
	}

	body := scrape(t)
	if !strings.Contains(body, "excalibase_provision_duration_seconds") {
		t.Error("expected excalibase_provision_duration_seconds in /metrics output")
	}
	if !strings.Contains(body, "excalibase_provision_errors_total") {
		t.Error("expected excalibase_provision_errors_total in /metrics output")
	}
}

func TestObserveProvisionSuccessDoesNotCountError(t *testing.T) {
	before := testutil.ToFloat64(provisionErrors)
	ObserveProvision(time.Now(), nil)
	if after := testutil.ToFloat64(provisionErrors); after != before {
		t.Errorf("success path bumped error counter: got %v, want %v", after, before)
	}
}
