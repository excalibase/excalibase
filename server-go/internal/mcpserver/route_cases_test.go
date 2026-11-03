package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

// routeCases is every tool's routing case: the long-standing ones and the
// tools added since.
func routeCases() []routeCase {
	cases := append(append(baseRouteCases(), appManageRouteCases()...), authFlowRouteCases()...)
	cases = append(cases, slowQueryRouteCases()...)
	cases = append(append(append(cases, sitesRouteCases()...), siteDomainRouteCases()...), siteBuildRouteCases()...)
	return append(append(cases, advisorRouteCases()...), dbAllowListRouteCases()...)
}

func slowQueryRouteCases() []routeCase {
	return []routeCase{
		{
			name: "get_slow_queries", tool: "get_slow_queries", args: map[string]any{"project_id": testProjectA, "sort": "mean", "limit": 5},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/slow-queries/", 200, `{"engine":"postgresql","enabled":true,"sort":"mean","queries":[]}`)
			},
			expect: []string{"GET " + provisionA + "/slow-queries/?limit=5&sort=mean"},
		},
		{
			name: "get_slow_queries by default", tool: "get_slow_queries", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/slow-queries/", 200, `{"engine":"postgresql","enabled":false,"sort":"total","queries":[]}`)
			},
			expect: []string{"GET " + provisionA + "/slow-queries/?sort=total"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if note, _ := out["next"].(string); !strings.Contains(note, "Studio") {
					t.Errorf("statistics off says nothing of how to turn them on: %v", out)
				}
			},
		},
	}
}

func appManageRouteCases() []routeCase {
	return []routeCase{
		{
			name: "update_app", tool: "update_app", args: map[string]any{"project_id": testProjectA, "app_id": "web", "port": 3000},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
				f.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","version":8}`)
			},
			expect: []string{"GET " + projectsA + "/apps/web/", "PATCH " + projectsA + "/apps/web/"},
		},
		{
			name: "delete_app", tool: "delete_app", args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodDelete, projectsA+"/apps/web/", 204, ``) },
			expect: []string{"DELETE " + projectsA + "/apps/web/"},
		},
	}
}
