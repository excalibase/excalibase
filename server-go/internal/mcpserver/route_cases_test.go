package mcpserver

import (
	"net/http"
	"strings"
)

// routeCases is every tool's routing case: the long-standing ones and the
// tools added since.
func routeCases() []routeCase {
	cases := append(append(baseRouteCases(), appManageRouteCases()...), authFlowRouteCases()...)
	return append(append(append(append(cases, sitesRouteCases()...), siteDomainRouteCases()...), siteBuildRouteCases()...), s3KeyRouteCases()...)
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

// testS3KeyID is shaped as a key id; repeated digits keep secret scanners quiet.
var testS3KeyID = "s3k_" + strings.Repeat("0", 32)

func s3KeyRouteCases() []routeCase {
	return []routeCase{
		{
			name: "list_s3_keys", tool: "list_s3_keys", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/storage/s3-keys", 200, `{"keys":[],"endpoint":"https://s3.example.test","region":"us-east-1"}`)
			},
			expect: []string{"GET " + projectsA + "/storage/s3-keys"},
		},
		{
			name: "revoke_s3_key", tool: "revoke_s3_key",
			args: map[string]any{"project_id": testProjectA, "key_id": testS3KeyID},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodDelete, projectsA+"/storage/s3-keys/"+testS3KeyID, 204, ``)
			},
			expect: []string{"DELETE " + projectsA + "/storage/s3-keys/" + testS3KeyID},
		},
	}
}
