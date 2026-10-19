package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const oomApp = `{"id":"web","name":"web","status":"ACTIVE","restarts":{"count":3,"lastReason":"OOMKilled","lastExitCode":137,"lastAt":"2026-10-19T12:00:00Z"}}`

// EXC-651: an app that was OOMKilled and restarted read ACTIVE in list_apps
// and get_deploy_status. Both carry the restarts and say what the stop means.
func TestListAppsSaysWhichAppsKeepRestarting(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/", 200, "["+oomApp+`,{"id":"api","name":"api","status":"ACTIVE","restarts":{"count":0}}]`)
	routes.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, `[{"id":"d1","status":"succeeded"}]`)
	routes.on(http.MethodGet, projectsA+"/apps/api/deploys", 200, `[{"id":"d2","status":"succeeded"}]`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "list_apps", map[string]any{"project_id": testProjectA}))
	apps, _ := out["apps"].([]any)
	first, _ := apps[0].(map[string]any)
	if restarts, _ := first["restarts"].(map[string]any); restarts["count"] != float64(3) || restarts["lastReason"] != "OOMKilled" {
		t.Fatalf("restarts = %v", first["restarts"])
	}
	warning := fmt.Sprint(out["restarted"])
	if !strings.Contains(warning, "web") || !strings.Contains(warning, "memory") || strings.Contains(warning, "api") {
		t.Fatalf("restarted = %v, want only web named with the memory cause", out["restarted"])
	}
}

func TestDeployStatusSaysASucceededAppKeepsRestarting(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, oomApp)
	routes.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, `[{"id":"d1","status":"succeeded"}]`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_deploy_status", map[string]any{"project_id": testProjectA, "app_id": "web"}))
	note, _ := out["restarted"].(string)
	if !strings.Contains(note, "3") || !strings.Contains(note, "OOMKilled") || !strings.Contains(note, "memory") {
		t.Fatalf("restarted = %q", note)
	}
}

// EXC-651: commit_sha was checked on the image path but dropped unchecked on a
// redeploy of the current image.
func TestDeployAppChecksTheCommitOnBothPaths(t *testing.T) {
	for _, args := range []map[string]any{
		{"project_id": testProjectA, "app_id": "web", "commit_sha": "v1"},
		{"project_id": testProjectA, "app_id": "web", "commit_sha": "v1", "image": "ghcr.io/a/web:1"},
	} {
		routes := newFakeRoutes()
		cs := session(t, routes, &recordingAudit{}, writeCaller())
		res := callTool(t, cs, "deploy_app", args)
		if !res.IsError || !strings.Contains(resultText(res), "7 to 64") || len(routes.calls) != 0 {
			t.Errorf("%v: %s (calls %d), want a refusal before any call", args, resultText(res), len(routes.calls))
		}
	}
}

func TestDeployAppKeepsTheCommitOfARedeploy(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"pending","digest":"sha256:aa"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	callTool(t, cs, "deploy_app", map[string]any{"project_id": testProjectA, "app_id": "web", "commit_sha": "abc1234"})
	if len(routes.calls) != 1 || routes.calls[0].Body != `{"commitSha":"abc1234"}` {
		t.Fatalf("calls = %+v, want the commit sent with the redeploy", routes.calls)
	}
}

func TestDeployAppDescriptionStatesTheCommitRule(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, listedTool := range listed.Tools {
		if listedTool.Name != "deploy_app" {
			continue
		}
		schema := fmt.Sprint(listedTool.InputSchema)
		if !strings.Contains(schema, "7 to 64 lowercase hex") {
			t.Errorf("deploy_app does not state the commit rule: %s", schema)
		}
	}
}

// EXC-651: a revoke on a query function tracked with inference has no effect:
// the role still calls it through its select on the return table. The answer
// and the description say so; the permission itself is unchanged.
func TestRevokingAnInferredQueryFunctionSaysItHasNoEffect(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodDelete, provisionA+"/function-permissions/public.search/roles/anon", 200, `{}`)
	routes.on(http.MethodGet, provisionA+"/permissions/", 200,
		`{"functions":[{"function":"public.search","exposedAs":"QUERY","inferPermissions":true,"sessionArgument":null}]}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "set_db_function_permission", map[string]any{
		"project_id": testProjectA, "function": "public.search", "role": "anon", "allowed": false,
	}))
	note, _ := out["effect"].(string)
	if out["allowed"] != false || !strings.Contains(note, "no effect") || !strings.Contains(note, "infer_permissions") {
		t.Fatalf("out = %v", out)
	}
}

func TestRevokingAFunctionWithoutInferenceSaysNothingMore(t *testing.T) {
	for _, tracked := range []string{
		`{"function":"public.search","exposedAs":"QUERY","inferPermissions":false}`,
		`{"function":"public.search","exposedAs":"MUTATION","inferPermissions":true}`,
	} {
		routes := newFakeRoutes()
		routes.on(http.MethodDelete, provisionA+"/function-permissions/public.search/roles/anon", 200, `{}`)
		routes.on(http.MethodGet, provisionA+"/permissions/", 200, `{"functions":[`+tracked+`]}`)
		cs := session(t, routes, &recordingAudit{}, writeCaller())
		out := structured(t, callTool(t, cs, "set_db_function_permission", map[string]any{
			"project_id": testProjectA, "function": "public.search", "role": "anon", "allowed": false,
		}))
		if out["effect"] != nil {
			t.Errorf("%s: effect = %v", tracked, out["effect"])
		}
	}
}

func TestFunctionPermissionDescriptionExplainsInference(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, listedTool := range listed.Tools {
		if listedTool.Name == "set_db_function_permission" && !strings.Contains(listedTool.Description, "infer_permissions") {
			t.Errorf("set_db_function_permission does not explain inference: %s", listedTool.Description)
		}
	}
}
