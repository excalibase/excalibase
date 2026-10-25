package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func serveMetricsKeyGate(token *domain.AccessToken, method, target string) (*httptest.ResponseRecorder, bool) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = auth.GetToken(r.Context()) == token
		w.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(method, target, nil)
	if token != nil {
		request = request.WithContext(auth.SetToken(auth.SetUser(request.Context(), &domain.User{ID: "u1"}), token))
	}
	recorder := httptest.NewRecorder()
	MetricsKeyGate(next).ServeHTTP(recorder, request)
	return recorder, reached
}

func metricsKey(projectID string) *domain.AccessToken {
	return &domain.AccessToken{UserID: "u1", Scopes: auth.ScopeMetricsRead, ProjectID: projectID}
}

// EXC-657: a metrics key reads its own project's metrics endpoint and nothing else.
func TestMetricsKeyGate_ReachesItsOwnProjectsEndpoint(t *testing.T) {
	for _, target := range []string{"/api/projects/p1/metrics", "/api/projects/p1/metrics/"} {
		recorder, reached := serveMetricsKeyGate(metricsKey("p1"), http.MethodGet, target)
		if !reached || recorder.Code != http.StatusOK {
			t.Errorf("GET %s: %d, reached=%v", target, recorder.Code, reached)
		}
	}
}

func TestMetricsKeyGate_RefusesEverythingElse(t *testing.T) {
	cases := []struct {
		name, method, target string
		key                  *domain.AccessToken
	}{
		{"another project", http.MethodGet, "/api/projects/p2/metrics", metricsKey("p1")},
		{"an unbound key", http.MethodGet, "/api/projects/p1/metrics", metricsKey("")},
		{"a write", http.MethodPost, "/api/projects/p1/metrics", metricsKey("p1")},
		{"a head", http.MethodHead, "/api/projects/p1/metrics", metricsKey("p1")},
		{"the studio metrics", http.MethodGet, "/api/provision/p1/metrics/current", metricsKey("p1")},
		{"the key list", http.MethodGet, "/api/projects/p1/metrics-keys", metricsKey("p1")},
		{"token minting", http.MethodPost, "/api/auth/tokens", metricsKey("p1")},
		{"mcp", http.MethodPost, "/mcp", metricsKey("p1")},
		{"a deeper path", http.MethodGet, "/api/projects/p1/metrics/x", metricsKey("p1")},
		{"a dot segment", http.MethodGet, "/api/projects/p2/../p1/metrics", metricsKey("p1")},
		{"an escaped path", http.MethodGet, "/api/projects/p1%2Fx/metrics", metricsKey("p1%2Fx")},
		{"health", http.MethodGet, "/healthz", metricsKey("p1")},
	}
	for _, tc := range cases {
		recorder, reached := serveMetricsKeyGate(tc.key, tc.method, tc.target)
		if reached || recorder.Code != http.StatusForbidden {
			t.Errorf("%s: %d, reached=%v", tc.name, recorder.Code, reached)
		}
	}
}

func TestMetricsKeyGate_LeavesOtherCallersAlone(t *testing.T) {
	for name, token := range map[string]*domain.AccessToken{
		"session":    {Scopes: auth.ScopeSession},
		"legacy pat": {},
		"read pat":   {Scopes: auth.ScopeRead, ProjectID: "p1"},
	} {
		recorder, reached := serveMetricsKeyGate(token, http.MethodGet, "/api/provision/p1")
		if !reached || recorder.Code != http.StatusOK {
			t.Errorf("%s: %d", name, recorder.Code)
		}
	}
	if recorder, _ := serveMetricsKeyGate(nil, http.MethodGet, "/api/provision/p1"); recorder.Code != http.StatusOK {
		t.Errorf("anonymous: %d", recorder.Code)
	}
}
