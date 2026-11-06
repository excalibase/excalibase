package mcpserver

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func monitoringRouteCases() []routeCase {
	return []routeCase{
		{
			name: "get_request_metrics", tool: "get_request_metrics", args: map[string]any{"project_id": testProjectA, "range": "24h"},
			features: []features.Feature{features.RequestMetrics},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/requests", 200, `{"range":"24h","series":[]}`)
			},
			expect: []string{"GET " + provisionA + "/requests?range=24h"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["range"] != "24h" {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_metrics", tool: "get_metrics", args: map[string]any{"project_id": testProjectA, "history": 10},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"cpuPercent":12.5}`)
				f.on(http.MethodGet, provisionA+"/metrics/history", 200, `[{"cpuPercent":11}]`)
			},
			expect: []string{"GET " + provisionA + "/metrics/current", "GET " + provisionA + "/metrics/history?limit=10"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				history, _ := out["history"].([]any)
				current, _ := out["current"].(map[string]any)
				if len(history) != 1 || current["cpuPercent"] != 12.5 {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_metrics without history", tool: "get_metrics", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"cpuPercent":1}`)
			},
			expect: []string{"GET " + provisionA + "/metrics/current"},
		},
		{
			name: "list_alerts", tool: "list_alerts", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, "/api/alerts/project/"+testProjectA+"/", 200, `[{"id":"a1","severity":"warning"}]`)
			},
			expect: []string{"GET /api/alerts/project/" + testProjectA + "/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if alerts, _ := out["alerts"].([]any); len(alerts) != 1 {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_database_info", tool: "get_database_info", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/cluster", 200, `{"storageSize":"10Gi"}`)
			},
			expect: []string{"GET " + provisionA + "/cluster"},
		},
		{
			name: "get_db_endpoint", tool: "get_db_endpoint", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-endpoint/", 200, `{"host":"db.example.test","port":5432}`)
			},
			expect: []string{"GET " + projectsA + "/db-endpoint/"},
		},
		{
			name: "get_db_read_address", tool: "get_db_read_address", args: map[string]any{"project_id": testProjectA},
			features: []features.Feature{features.DBReadAddress},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-read-address/", 200, `{"replicas":1,"enabled":true}`)
			},
			expect: []string{"GET " + projectsA + "/db-read-address/"},
		},
	}
}
