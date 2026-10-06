package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

const createdApp = `{"id":"app-1","name":"web","status":"PROVISIONING","url":"https://web-proj-a.apps.example.test:443/"}`

func createWeb(t *testing.T, routes *fakeRoutes) map[string]any {
	t.Helper()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	return structured(t, callTool(t, cs, "create_app", map[string]any{"project_id": testProjectA, "name": "web", "image": "ghcr.io/a/web:1"}))
}

func TestCreateAppLeavesAWildcardAllowlistAlone(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, createdApp)
	routes.on(http.MethodGet, projectsA+"/cors/", 200, `{"allowedOrigins":["*"],"allowWildcard":true}`)
	out := createWeb(t, routes)
	if len(routes.calls) != 2 || out["corsOriginAdded"] != nil {
		t.Fatalf("calls %+v out %v", routes.calls, out)
	}
}

func TestCreateAppComparesOriginsTheWayTheServerStoresThem(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, createdApp)
	routes.on(http.MethodGet, projectsA+"/cors/", 200, `{"allowedOrigins":["https://web-proj-a.apps.example.test"],"allowWildcard":false}`)
	createWeb(t, routes)
	if len(routes.calls) != 2 {
		t.Fatalf("a default port is the same origin, nothing to add: %+v", routes.calls)
	}
}

func TestCreateAppKeepsTheAppWhenTheAllowlistCannotChange(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, createdApp)
	routes.on(http.MethodGet, projectsA+"/cors/", 200, `{"allowedOrigins":[],"allowWildcard":false}`)
	routes.on(http.MethodPut, projectsA+"/cors/", 503, `{"error":"cors allowlist not configured"}`)
	out := createWeb(t, routes)
	app, _ := out["app"].(map[string]any)
	if app["id"] != "app-1" || !strings.Contains(out["corsError"].(string), "cors allowlist not configured") {
		t.Fatalf("out = %v", out)
	}
}

func TestCreateAppWithoutAnAddressTouchesNoAllowlist(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, `{"id":"app-1","name":"web","status":"PROVISIONING"}`)
	createWeb(t, routes)
	if len(routes.calls) != 1 {
		t.Fatalf("calls = %+v", routes.calls)
	}
}

func TestListAppsKeepsGoingWhenOneDeployLookupFails(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/", 200, `[{"id":"a","status":"PROVISIONING"},{"id":"b","status":"PROVISIONING"}]`)
	routes.on(http.MethodGet, projectsA+"/apps/b/deploys", 200, `[]`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "list_apps", map[string]any{"project_id": testProjectA}))
	apps, _ := out["apps"].([]any)
	first, _ := apps[0].(map[string]any)
	second, _ := apps[1].(map[string]any)
	if first["status"] != "PROVISIONING" || second["status"] != "NOT_DEPLOYED" {
		t.Fatalf("apps = %v", apps)
	}
}

func TestDeployStatusWithNoDeploysListsNone(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","status":"RUNNING"}`)
	routes.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, `[]`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_deploy_status", map[string]any{"project_id": testProjectA, "app_id": "web"}))
	if deploys, ok := out["deploys"].([]any); !ok || len(deploys) != 0 {
		t.Fatalf("deploys = %#v", out["deploys"])
	}
}
