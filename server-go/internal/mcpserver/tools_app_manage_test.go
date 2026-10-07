package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const storedWeb = `{"id":"web","name":"web","image":"ghcr.io/a/web:1","port":8080,"replicas":1,"status":"ACTIVE","version":7,` +
	`"env":[{"name":"KEEP","kind":"literal","value":"1"},{"name":"DB","kind":"secret","secret":{"path":"projects/p/apps/web/DB"}},{"name":"OLD","kind":"literal","value":"x"}]}`

func updateSession(t *testing.T, routes *fakeRoutes) func(args map[string]any) map[string]any {
	t.Helper()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	return func(args map[string]any) map[string]any {
		args["project_id"], args["app_id"] = testProjectA, "web"
		return structured(t, callTool(t, cs, "update_app", args))
	}
}

func TestUpdateAppSendsOnlyWhatChangedAtTheVersionItRead(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
	routes.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","name":"web","image":"ghcr.io/a/web:2","port":8080,"replicas":2,"version":8}`)
	out := updateSession(t, routes)(map[string]any{"image": "ghcr.io/a/web:2", "replicas": 2})
	patch := routes.calls[1]
	if patch.Method != http.MethodPatch || patch.Header.Get("If-Match") != "7" {
		t.Fatalf("patch = %+v", patch)
	}
	if patch.Body != `{"image":"ghcr.io/a/web:2","replicas":2}` {
		t.Fatalf("only the named fields are sent: %s", patch.Body)
	}
	if !strings.Contains(out["next"].(string), "deploy_app") || out["app"] == nil {
		t.Fatalf("out = %v", out)
	}
}

func TestUpdateAppMergesEnvAndKeepsSecretsUntouched(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
	routes.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","version":8}`)
	updateSession(t, routes)(map[string]any{
		"set_env":    []map[string]any{{"name": "KEEP", "value": "2"}, {"name": "NEW", "value": "n"}},
		"remove_env": []string{"OLD"},
	})
	var body struct {
		Env []map[string]any `json:"env"`
	}
	if err := json.Unmarshal([]byte(routes.calls[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, variable := range body.Env {
		got = append(got, variable["name"].(string)+"="+variable["kind"].(string))
	}
	if strings.Join(got, ",") != "DB=secret,KEEP=literal,NEW=literal" {
		t.Fatalf("env = %v", got)
	}
	if !strings.Contains(routes.calls[1].Body, `"path":"projects/p/apps/web/DB"`) {
		t.Fatalf("the stored secret reference must be sent back as it was: %s", routes.calls[1].Body)
	}
}

func TestUpdateAppRefusesAnEmptyChange(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	if res := callTool(t, cs, "update_app", map[string]any{"project_id": testProjectA, "app_id": "web"}); !res.IsError {
		t.Fatal("an update that changes nothing must say so")
	}
}

func TestUpdateAppPrivateNetworkIsTheProjectSwitch(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPut, projectsA+"/app-network/", 200, `{"privateNetwork":true,"applied":true}`)
	out := updateSession(t, routes)(map[string]any{"private_network": true})
	if len(routes.calls) != 1 || routes.calls[0].Body != `{"privateNetwork":true}` || out["privateNetwork"] != true {
		t.Fatalf("calls %+v out %v", routes.calls, out)
	}
}

func TestUpdateAppSaysWhenTheNetworkStepFailedAfterTheSettingsWereStored(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
	routes.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","version":8}`)
	routes.on(http.MethodPut, projectsA+"/app-network/", 403, `{"error":"admin role required"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "update_app", map[string]any{"project_id": testProjectA, "app_id": "web", "port": 3000, "private_network": true})
	if !res.IsError || !strings.Contains(resultText(res), "were stored") || !strings.Contains(resultText(res), "admin role required") {
		t.Fatalf("result = %s", resultText(res))
	}
}

func TestUpdateAppPassesThePlanRefusalOn(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
	routes.on(http.MethodPatch, projectsA+"/apps/web/", 400, `{"error":"replicas must be between 0 and 1 on the free plan"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "update_app", map[string]any{"project_id": testProjectA, "app_id": "web", "replicas": 3})
	if !res.IsError || !strings.Contains(resultText(res), "free plan") {
		t.Fatalf("result = %s", resultText(res))
	}
}

func TestDeleteAppDeletesOnlyThatApp(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodDelete, projectsA+"/apps/web/", 204, ``)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "delete_app", map[string]any{"project_id": testProjectA, "app_id": "web"}))
	if len(routes.calls) != 1 || routes.calls[0].Body != "" || out["deleted"] != "web" {
		t.Fatalf("calls %+v out %v", routes.calls, out)
	}
}

func TestDeleteAppWithADiskIsRefusedByTheRoute(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodDelete, projectsA+"/apps/web/", 409, `{"error":"the app has a disk; confirmDeleteDisk is required"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "delete_app", map[string]any{"project_id": testProjectA, "app_id": "web"})
	if !res.IsError || !strings.Contains(resultText(res), "disk") {
		t.Fatalf("result = %s", resultText(res))
	}
}
