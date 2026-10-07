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

func TestCreateAppAddsTheOriginTheWayTheServerStoresIt(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, createdApp)
	routes.on(http.MethodPost, projectsA+"/cors/origins", 200, `{"allowedOrigins":["*"],"allowWildcard":true,"added":false}`)
	out := createWeb(t, routes)
	if len(routes.calls) != 2 || out["corsOriginAdded"] != nil {
		t.Fatalf("calls %+v out %v", routes.calls, out)
	}
	if body := routes.calls[1].Body; body != `{"appId":"app-1","origin":"https://web-proj-a.apps.example.test"}` {
		t.Fatalf("a default port is the same origin: %s", body)
	}
}

func TestCreateAppKeepsTheAppWhenTheAllowlistCannotChange(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/", 201, createdApp)
	routes.on(http.MethodPost, projectsA+"/cors/origins", 503, `{"error":"cors allowlist not configured"}`)
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

func TestDeployAppSaysWhenADeployRunsUnpinned(t *testing.T) {
	for body, wantNote := range map[string]bool{
		`{"id":"d1","status":"pending","image":"registry.local:5000/web:1"}`:                                                 true,
		`{"id":"d1","status":"pending","image":"ghcr.io/a/web@sha256:aa","imageRef":"ghcr.io/a/web:1","digest":"sha256:aa"}`: false,
	} {
		routes := newFakeRoutes()
		routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, body)
		cs := session(t, routes, &recordingAudit{}, writeCaller())
		out := structured(t, callTool(t, cs, "deploy_app", map[string]any{"project_id": testProjectA, "app_id": "web"}))
		note, _ := out["unpinned"].(string)
		if wantNote != (note != "") || (wantNote && !strings.Contains(note, "public")) {
			t.Errorf("%s: unpinned = %q", body, note)
		}
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

func TestDeployStatusSaysWhenTheCertificateIsNotReadyYet(t *testing.T) {
	for cert, want := range map[string]string{
		`{"hostname":"web-proj-a.apps.example.test","status":"issuing"}`:                             "certificate_pending",
		`{"hostname":"web-proj-a.apps.example.test","status":"issue_failed","failureReason":"rate"}`: "certificate_failed",
		`{"hostname":"web-proj-a.apps.example.test","status":"active"}`:                              "ready",
		`{"status":"none"}`: "",
	} {
		routes := newFakeRoutes()
		routes.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","status":"RUNNING","url":"https://web-proj-a.apps.example.test"}`)
		routes.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, `[{"id":"d2","status":"succeeded"},{"id":"d1","status":"failed"}]`)
		routes.on(http.MethodGet, projectsA+"/apps/web/certificate", 200, cert)
		cs := session(t, routes, &recordingAudit{}, writeCaller())
		out := structured(t, callTool(t, cs, "get_deploy_status", map[string]any{"project_id": testProjectA, "app_id": "web"}))
		https, _ := out["https"].(map[string]any)
		if got, _ := https["status"].(string); got != want {
			t.Errorf("%s: https = %v", cert, out["https"])
		}
		if want == "certificate_pending" && !strings.Contains(https["note"].(string), "browsers refuse") {
			t.Errorf("note = %v", https["note"])
		}
	}
}

func TestDeployStatusSkipsTheCertificateUntilADeploySucceeds(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","status":"DEPLOYING","url":"https://web-proj-a.apps.example.test"}`)
	routes.on(http.MethodGet, projectsA+"/apps/web/deploys/d2", 200, `{"id":"d2","status":"rolling_out"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_deploy_status", map[string]any{"project_id": testProjectA, "app_id": "web", "deploy_id": "d2"}))
	if len(routes.calls) != 2 || out["https"] != nil {
		t.Fatalf("calls %+v out %v", routes.calls, out)
	}
}

func TestDeployWithoutAnImageSaysItRunsTheSameDigest(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"pending","image":"ghcr.io/a/web@sha256:aa","digest":"sha256:aa"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "deploy_app", map[string]any{"project_id": testProjectA, "app_id": "web"}))
	if note, _ := out["image"].(string); !strings.Contains(note, "Pass the tag") {
		t.Fatalf("out = %v", out)
	}
	out = structured(t, callTool(t, cs, "deploy_app", map[string]any{"project_id": testProjectA, "app_id": "web", "image": "ghcr.io/a/web:2"}))
	if out["image"] != nil {
		t.Fatalf("an explicit image needs no note: %v", out)
	}
}
