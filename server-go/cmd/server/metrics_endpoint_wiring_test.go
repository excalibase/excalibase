package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/promexport"
)

type storedTotals struct{ rows []domain.RequestTotalsRow }

func (s storedTotals) Snapshot(_ context.Context, projectID string) (promexport.Snapshot, error) {
	return promexport.Snapshot{ProjectID: projectID, Now: time.Now(), Requests: s.rows}, nil
}

// metricsEndpointRouter is the production router with the metrics endpoint
// over fixed totals for both matrix projects.
func metricsEndpointRouter(t *testing.T, on ...features.Feature) (http.Handler, callers) {
	t.Helper()
	totals := storedTotals{rows: []domain.RequestTotalsRow{
		{ProjectID: matrixProjectA, Family: "graphql", Requests: 7},
		{ProjectID: matrixProjectB, Family: "graphql", Requests: 9},
	}}
	router, _, admins, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		deps.features = features.NewStatic(on...)
		deps.metricsKeysHandler = handler.NewMetricsKeysHandler(platform.Tokens, nil)
		deps.metricsEndpointHandler = handler.NewMetricsEndpointHandler(totals)
		deps.rlMetricsScrape = custommw.RateLimit(custommw.PerProjectAndUser, 1000, time.Minute)
	})
	return router, admins
}

func send(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// mintMetricsKey makes a metrics key for project A through the real route.
func mintMetricsKey(t *testing.T, router http.Handler, admins callers) string {
	t.Helper()
	created := send(router, http.MethodPost, "/api/projects/"+matrixProjectA+"/metrics-keys", admins[callerAdminSession], `{"name":"grafana"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var issued struct{ Token string }
	if err := json.Unmarshal(created.Body.Bytes(), &issued); err != nil || issued.Token == "" {
		t.Fatalf("issued: %s", created.Body)
	}
	return issued.Token
}

// EXC-657: a metrics key reads its own project's endpoint, and only that.
func TestMetricsKey_ReadsOnlyItsOwnProjectsEndpoint(t *testing.T) {
	router, admins := metricsEndpointRouter(t, features.MetricsEndpoint)
	key := mintMetricsKey(t, router, admins)

	own := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics", key, "")
	if own.Code != http.StatusOK || own.Header().Get("Content-Type") != promexport.ContentType {
		t.Fatalf("own endpoint: %d %s", own.Code, own.Body)
	}
	if !strings.Contains(own.Body.String(), `project="`+matrixProjectA+`"`) || strings.Contains(own.Body.String(), matrixProjectB) {
		t.Errorf("only project A's metrics:\n%s", own.Body)
	}

	refused := []struct{ method, path string }{
		{http.MethodGet, "/api/projects/" + matrixProjectB + "/metrics"},
		{http.MethodGet, "/api/provision/" + matrixProjectA + "/"},
		{http.MethodGet, "/api/provision/" + matrixProjectA + "/metrics/current"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/metrics-keys"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/metrics-keys"},
		{http.MethodGet, "/api/auth/tokens"},
		{http.MethodPost, "/api/auth/tokens"},
		{http.MethodGet, "/api/schema/" + matrixProjectA + "/tables"},
	}
	for _, probe := range refused {
		if got := send(router, probe.method, probe.path, key, `{"name":"x"}`); got.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", probe.method, probe.path, got.Code)
		}
	}
}

func TestMetricsKey_NeverInTheQueryString(t *testing.T) {
	router, admins := metricsEndpointRouter(t, features.MetricsEndpoint)
	key := mintMetricsKey(t, router, admins)
	for _, param := range []string{"token", "access_token", "key"} {
		got := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics?"+param+"="+key, "", "")
		if got.Code != http.StatusUnauthorized {
			t.Errorf("?%s=: %d", param, got.Code)
		}
	}
}

func TestMetricsKey_RevokedIsRefused(t *testing.T) {
	router, admins := metricsEndpointRouter(t, features.MetricsEndpoint)
	key := mintMetricsKey(t, router, admins)
	listed := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics-keys", admins[callerAdminSession], "")
	var keys []struct{ ID string }
	if err := json.Unmarshal(listed.Body.Bytes(), &keys); err != nil || len(keys) != 1 {
		t.Fatalf("list: %s", listed.Body)
	}
	if got := send(router, http.MethodDelete, "/api/projects/"+matrixProjectA+"/metrics-keys/"+keys[0].ID, admins[callerAdminSession], ""); got.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", got.Code)
	}
	if got := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics", key, ""); got.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", got.Code)
	}
}

// Only Admins make or revoke keys; any member may read the endpoint itself.
func TestMetricsKeys_AreForAdmins(t *testing.T) {
	router, who, _, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		deps.features = features.NewStatic(features.MetricsEndpoint)
		deps.metricsKeysHandler = handler.NewMetricsKeysHandler(platform.Tokens, nil)
		deps.metricsEndpointHandler = handler.NewMetricsEndpointHandler(storedTotals{})
		deps.rlMetricsScrape = custommw.RateLimit(custommw.PerProjectAndUser, 1000, time.Minute)
	})
	keys := "/api/projects/" + matrixProjectA + "/metrics-keys"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if got := send(router, method, keys, who[callerDeveloper], `{"name":"g"}`); got.Code != http.StatusForbidden {
			t.Errorf("developer %s: %d", method, got.Code)
		}
	}
	if got := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics", who[callerDeveloper], ""); got.Code != http.StatusOK {
		t.Errorf("a member reads the endpoint with their session: %d", got.Code)
	}
	if got := send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics", who[callerOtherOrg], ""); got.Code != http.StatusNotFound {
		t.Errorf("a non-member: %d", got.Code)
	}
}

// With the flag off nothing answers: no endpoint, no key routes.
func TestMetricsEndpoint_FlagOffIsNotMounted(t *testing.T) {
	router, admins := metricsEndpointRouter(t)
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/metrics"},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/metrics-keys"},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/metrics-keys"},
	} {
		got := send(router, probe.method, probe.path, admins[callerAdminSession], `{"name":"g"}`)
		if got.Code != http.StatusNotFound || got.Body.String() != unmountedBody {
			t.Errorf("%s %s: %d %q", probe.method, probe.path, got.Code, got.Body)
		}
	}
}

// The scrape budget is per key holder and project, separate from Studio's.
func TestMetricsEndpoint_IsRateLimited(t *testing.T) {
	router, _, admins, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		deps.features = features.NewStatic(features.MetricsEndpoint)
		deps.metricsKeysHandler = handler.NewMetricsKeysHandler(platform.Tokens, nil)
		deps.metricsEndpointHandler = handler.NewMetricsEndpointHandler(storedTotals{})
		deps.rlMetricsScrape = newMetricsScrapeLimiter()
	})
	key := mintMetricsKey(t, router, admins)
	statuses := map[int]int{}
	for i := 0; i <= metricsScrapesPerMinute; i++ {
		statuses[send(router, http.MethodGet, "/api/projects/"+matrixProjectA+"/metrics", key, "").Code]++
	}
	if statuses[http.StatusOK] != metricsScrapesPerMinute || statuses[http.StatusTooManyRequests] != 1 {
		t.Errorf("statuses: %v", statuses)
	}
}

// The running totals are only kept while the endpoint is on.
func TestMetricsTotals_KeptOnlyWithTheFeature(t *testing.T) {
	totals := &fakeRequestTotals{}
	if metricsEndpointTotals(config.AppConfig{}, totals) != nil {
		t.Error("off: no totals")
	}
	on := config.AppConfig{DarkFeatures: []features.Feature{features.MetricsEndpoint}}
	if metricsEndpointTotals(on, totals) == nil {
		t.Error("on: totals")
	}
	if metricsEndpointTotals(on, struct{}{}) != nil {
		t.Error("no table: no totals")
	}
}

type fakeRequestTotals struct{}

func (fakeRequestTotals) AddRequestTotals(context.Context, []domain.RequestMetricsRow) error {
	return nil
}

func (fakeRequestTotals) RequestTotals(context.Context, string) ([]domain.RequestTotalsRow, error) {
	return nil, nil
}

func (fakeRequestTotals) PruneRequestTotalsBefore(context.Context, time.Time) error { return nil }
