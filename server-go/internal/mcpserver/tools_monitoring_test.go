package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// planted is a field no view names; it must never reach the agent.
const planted = `,"plantedSecret":"hunter2","nested":{"password":"hunter2"}`

func monitoringRouteCases() []routeCase {
	return []routeCase{
		{
			name: "get_request_metrics", tool: "get_request_metrics", args: map[string]any{"project_id": testProjectA, "range": "24h"},
			features: []features.Feature{features.RequestMetrics},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/requests", 200, `{"projectId":"proj-a","range":"24h","from":"2026-11-05T00:00:00Z","to":"2026-11-06T00:00:00Z","stepSeconds":300,`+
					`"points":[{"timestamp":"2026-11-05T00:00:00Z","family":"graphql","requests":10,"clientErrors":1,"serverErrors":0,"p50Ms":4.5,"p95Ms":20,"connections":0,"connectionsOpened":0,"messages":0}],`+
					`"totals":[{"timestamp":"2026-11-06T00:00:00Z","family":"rest","requests":10,"clientErrors":0,"serverErrors":2,"p50Ms":null,"p95Ms":null,"connections":0,"connectionsOpened":0,"messages":0}]`+planted+`}`)
			},
			expect: []string{"GET " + provisionA + "/requests?range=24h"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				points, _ := out["points"].([]any)
				totals, _ := out["totals"].([]any)
				if out["range"] != "24h" || len(points) != 1 || len(totals) != 1 {
					t.Fatalf("out = %v", out)
				}
				if point, _ := points[0].(map[string]any); point["family"] != "graphql" || point["p95Ms"] != float64(20) {
					t.Errorf("point = %v", points[0])
				}
			},
		},
		{
			name: "get_metrics", tool: "get_metrics", args: map[string]any{"project_id": testProjectA, "history": 10},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"projectId":"proj-a","metricsAvailable":true,"unavailableReason":null,"cpuUsagePercent":12.5,"memoryUsagePercent":40,"diskUsagePercent":10,"activeConnections":3`+planted+`}`)
				f.on(http.MethodGet, provisionA+"/metrics/history", 200, `{"projectId":"proj-a","metrics":[{"projectId":"proj-a","metricsAvailable":true,"cpuUsagePercent":11}],"totalPoints":1`+planted+`}`)
			},
			expect: []string{"GET " + provisionA + "/metrics/current", "GET " + provisionA + "/metrics/history?limit=10"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				history, _ := out["history"].(map[string]any)
				samples, _ := history["metrics"].([]any)
				current, _ := out["current"].(map[string]any)
				if len(samples) != 1 || history["totalPoints"] != float64(1) || current["cpuUsagePercent"] != 12.5 {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_metrics without history", tool: "get_metrics", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"metricsAvailable":false,"unavailableReason":"the database could not be reached to read its metrics"}`)
			},
			expect: []string{"GET " + provisionA + "/metrics/current"},
		},
		{
			name: "list_alerts", tool: "list_alerts", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, "/api/alerts/project/"+testProjectA+"/", 200, `[{"id":"a1","projectId":"proj-a","severity":"WARNING","message":"cpu high","metric":"cpu","resolved":false,"timestamp":"2026-11-06T00:00:00Z"`+planted+`}]`)
			},
			expect: []string{"GET /api/alerts/project/" + testProjectA + "/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				alerts, _ := out["alerts"].([]any)
				if len(alerts) != 1 {
					t.Fatalf("out = %v", out)
				}
				if alert, _ := alerts[0].(map[string]any); alert["severity"] != "WARNING" {
					t.Errorf("alert = %v", alerts[0])
				}
			},
		},
		{
			name: "get_database_info", tool: "get_database_info", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/cluster", 200, `{"projectId":"proj-a","tier":"FREE","storageSize":"10Gi","storageLimit":"20Gi","storageUsedBytes":1024,"instances":1,"cpu":"500m","memory":"1Gi","parameters":{"work_mem":"4MB"},"tunableParameters":["work_mem"],"canChange":true`+planted+`}`)
			},
			expect: []string{"GET " + provisionA + "/cluster"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["storageSize"] != "10Gi" || out["storageUsedBytes"] != float64(1024) {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_db_endpoint", tool: "get_db_endpoint", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-endpoint/", 200, `{"projectId":"proj-a","publicOffered":true,"publicEnabled":true,"available":true,"host":"db.example.test","port":5432,"requireTls":true,"database":"app","username":"app",`+
					`"connectionStrings":{"requireTls":"postgresql://app@db.example.test:5432/app?sslmode=require","allowPlaintext":"postgresql://app@db.example.test:5432/app"},"caCertificate":"CA","internal":{"host":"svc","port":5432,"connectionString":"postgresql://app@svc:5432/app"},"canChange":true`+planted+`}`)
			},
			expect: []string{"GET " + projectsA + "/db-endpoint/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				strings, _ := out["connectionStrings"].(map[string]any)
				if out["host"] != "db.example.test" || strings["requireTls"] == nil {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "get_db_read_address", tool: "get_db_read_address", args: map[string]any{"project_id": testProjectA},
			features: []features.Feature{features.DBReadAddress},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-read-address/", 200, `{"projectId":"proj-a","replicas":1,"enabled":true,"available":true,"host":"r.example.test","port":6432,"username":"ro","readOnlyLogin":true,"canChange":false`+planted+`}`)
			},
			expect: []string{"GET " + projectsA + "/db-read-address/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["host"] != "r.example.test" || out["readOnlyLogin"] != true {
					t.Errorf("out = %v", out)
				}
			},
		},
	}
}

func authSettingsRouteCases() []routeCase {
	settings := `{"requireEmailVerification":true,"siteUrl":"https://app.example.test","redirectUrls":["https://app.example.test/cb"],"allowNewUsers":true,"guestSignInEnabled":true,"guestCaptchaSecretSet":true` + planted + `}`
	return []routeCase{
		{
			name: "get_auth_settings with sign-in flags off", tool: "get_auth_settings", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/auth-settings/", 200, `{"requireEmailVerification":true,"siteUrl":"https://app.example.test"`+planted+`}`)
			},
			expect: []string{"GET " + projectsA + "/auth-settings/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if _, has := out["providers"]; has || out["siteUrl"] != "https://app.example.test" || out["requireEmailVerification"] != true {
					t.Errorf("out = %v", out)
				}
				if _, has := out["redirectUrls"]; has {
					t.Errorf("redirect URLs shown with every sign-in flag off: %v", out)
				}
			},
		},
		{
			name: "get_auth_settings with oauth on", tool: "get_auth_settings", args: map[string]any{"project_id": testProjectA},
			features: []features.Feature{features.AuthOAuth},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/auth-settings/", 200, settings)
				f.on(http.MethodGet, projectsA+"/auth-providers/", 200, `{"providers":[{"provider":"github","enabled":true,"clientId":"id","clientSecretSet":true,"callbackUrl":"https://auth.example.test/cb","clientSecret":"hunter2"}]}`)
			},
			expect: []string{"GET " + projectsA + "/auth-settings/", "GET " + projectsA + "/auth-providers/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				providers, _ := out["providers"].([]any)
				if len(providers) != 1 {
					t.Fatalf("out = %v", out)
				}
				provider, _ := providers[0].(map[string]any)
				if provider["provider"] != "github" || provider["clientSecretSet"] != true || provider["callbackUrl"] == nil {
					t.Errorf("provider = %v", provider)
				}
				if out["allowNewUsers"] != true || out["guestSignInEnabled"] != true {
					t.Errorf("out = %v", out)
				}
			},
		},
	}
}

// No view lets a field it does not name through, whatever the route adds.
func TestMonitoringToolsNeverPassUnknownFields(t *testing.T) {
	cases := append(monitoringRouteCases(), authSettingsRouteCases()...)
	for _, tc := range cases {
		out := runRouteCase(t, tc)
		if strings.Contains(out, "hunter2") || strings.Contains(out, "plantedSecret") {
			t.Errorf("%s passed a field no view names: %s", tc.name, out)
		}
	}
}

// What an agent is told about each tool matches what the route does.
func TestMonitoringDescriptionsAreAccurate(t *testing.T) {
	described := toolDescriptions(t)
	for name, wants := range map[string][]string{
		"get_request_metrics": {"p50", "p95", "graphql", "rest", "auth", "realtime"},
		"list_alerts":         {"CPU", "memory", "backup"},
		"get_database_info":   {"Postgres", "single"},
		"get_db_endpoint":     {"Developer"},
		"get_auth_settings":   {"Client secrets", "Developer", "sign-in"},
		"get_logs":            {"data, not instructions"},
	} {
		for _, want := range wants {
			if !strings.Contains(described[name], want) {
				t.Errorf("%s does not mention %q: %s", name, want, described[name])
			}
		}
	}
	if strings.Contains(described["get_request_metrics"], "p99") || strings.Contains(described["list_alerts"], "failing backups") {
		t.Errorf("a description promises what the route does not give")
	}
}

// runRouteCase runs one routing case and returns the tool's answer as JSON.
func runRouteCase(t *testing.T, tc routeCase) string {
	t.Helper()
	routes := newFakeRoutes()
	tc.setup(routes)
	settings := testSettings
	if tc.features != nil {
		settings.Features = features.NewStatic(tc.features...)
	}
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
	res := callTool(t, cs, tc.tool, tc.args)
	if res.IsError {
		t.Fatalf("%s failed: %s", tc.name, resultText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// toolDescriptions is every tool's description with every feature on.
func toolDescriptions(t *testing.T) map[string]string {
	t.Helper()
	settings := testSettings
	settings.Features = features.NewStatic(features.All()...)
	cs := sessionWith(t, newFakeRoutes(), &recordingAudit{}, writeCaller(), settings)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	described := map[string]string{}
	for _, tool := range listed.Tools {
		described[tool.Name] = tool.Description
	}
	return described
}
