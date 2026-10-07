package mcpserver

import "net/http"

// routeCases is every tool's routing case: the long-standing ones and the
// tools added since.
func routeCases() []routeCase {
	return append(append(baseRouteCases(), appManageRouteCases()...), authFlowRouteCases()...)
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
