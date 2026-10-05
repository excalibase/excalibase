package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedactQueryParamsHidesTheValueFromTheLogLineOnly(t *testing.T) {
	var logged, handled string
	logger := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logged = r.RequestURI
			next.ServeHTTP(w, r)
		})
	}
	final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { handled = r.URL.Query().Get("sql") })
	handler := RedactQueryParams("sql")(logger(final))

	req := httptest.NewRequest(http.MethodGet, "/api/schema/p/query?sql=select+email+from+users&x=1", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if logged != "/api/schema/p/query?sql=REDACTED&x=1" {
		t.Errorf("logged %q", logged)
	}
	if handled != "select email from users" {
		t.Errorf("the handler must still read the value, got %q", handled)
	}
}

func TestRedactQueryParamsLeavesOtherRequestsAlone(t *testing.T) {
	var logged string
	handler := RedactQueryParams("sql")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { logged = r.RequestURI }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x?limit=5", nil))
	if logged != "/api/x?limit=5" {
		t.Errorf("logged %q", logged)
	}
}
