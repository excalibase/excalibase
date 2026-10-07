package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestCorsToolsNeverOpenTheProjectToEveryOrigin(t *testing.T) {
	routes := newFakeRoutes()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	for _, origin := range []string{"*", "https://*.example.test", "  "} {
		for _, name := range []string{"add_cors_origin", "remove_cors_origin"} {
			if res := callTool(t, cs, name, map[string]any{"project_id": testProjectA, "origin": origin}); !res.IsError {
				t.Errorf("%s %q was not refused", name, origin)
			}
		}
	}
	if len(routes.calls) != 0 {
		t.Fatalf("a refused origin reached the API: %+v", routes.calls)
	}
}

// TestNewToolsPassTheAPIsRefusalOn pins that a refused call is the tool's
// error, never a success with an empty answer.
func TestNewToolsPassTheAPIsRefusalOn(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	for name, args := range map[string]map[string]any{
		"list_cors_origins":           {},
		"add_cors_origin":             {"origin": "http://localhost:5173"},
		"remove_cors_origin":          {"origin": "http://localhost:5173"},
		"set_realtime":                {"table": "todos", "enabled": true},
		"delete_function":             {"id": "shorten"},
		"set_function_outbound_hosts": {"add": []string{"api.stripe.com"}},
		"track_db_function":           {"function": "public.short_code"},
		"set_db_function_permission":  {"function": "public.short_code", "role": "anon", "allowed": true},
		"set_function_secret":         {"key": "K", "value": "v"},
	} {
		args["project_id"] = testProjectA
		if res := callTool(t, cs, name, args); !res.IsError {
			t.Errorf("%s answered a refused call as a success", name)
		}
		args["project_id"] = "proj-elsewhere"
		if res := callTool(t, cs, name, args); !res.IsError {
			t.Errorf("%s reached a project the caller cannot", name)
		}
	}
}

func TestProjectInfoFallsBackToItsOwnOriginsWhenTheAllowlistIsUnreadable(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme","corsAllowedOrigins":["https://web.example.test"]}`)
	routes.on(http.MethodGet, projectsA+"/cors/", 403, `{"error":"forbidden"}`)
	routes.on(http.MethodGet, projectsA+"/sdk-keys/", 200, `{"keys":[]}`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	out := structured(t, callTool(t, cs, "get_project_info", map[string]any{"project_id": testProjectA}))
	cors, _ := out["cors"].(map[string]any)
	if origins, _ := cors["allowedOrigins"].([]any); len(origins) != 1 {
		t.Fatalf("cors = %v", cors)
	}
	if out["graphqlFieldsError"] == nil {
		t.Errorf("unreadable tables are said, not hidden: %v", out)
	}
}

func TestListCorsOriginsAnswersAnEmptyListNotNull(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/cors/", 200, `{"allowWildcard":false}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "list_cors_origins", map[string]any{"project_id": testProjectA}))
	if origins, ok := out["allowedOrigins"].([]any); !ok || len(origins) != 0 {
		t.Fatalf("allowedOrigins = %#v", out["allowedOrigins"])
	}
}

func TestTrackDBFunctionReportsTheRoleItCouldNotGrant(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, "/api/provision/proj-a/tracked-functions/", 201, `{"function":"public.short_code"}`)
	routes.on(http.MethodPut, "/api/provision/proj-a/function-permissions/public.short_code/roles/anon", 200, `{}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "track_db_function", map[string]any{
		"project_id": testProjectA, "function": "public.short_code", "roles": []string{"anon", "editor"},
	})
	if text := resultText(res); !res.IsError || !strings.Contains(text, "[anon] may call it, but granting editor failed") {
		t.Fatalf("a failed grant is the tool's error: %s", text)
	}
	if res := callTool(t, cs, "track_db_function", map[string]any{"project_id": testProjectA, "function": "short_code"}); !res.IsError {
		t.Error("an unqualified function was not refused")
	}
	if res := callTool(t, cs, "track_db_function", map[string]any{"project_id": testProjectA, "function": "public.f", "roles": []string{"a/b"}}); !res.IsError {
		t.Error("a role that is no path segment was not refused before tracking")
	}
}

func TestTrackDBFunctionGrantsRolesOnAFunctionTrackedBefore(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, "/api/provision/proj-a/tracked-functions/", 409, `{"error":"already tracked"}`)
	routes.on(http.MethodPut, "/api/provision/proj-a/function-permissions/public.short_code/roles/user", 200, `{}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "track_db_function", map[string]any{
		"project_id": testProjectA, "function": "public.short_code", "roles": []string{"user"},
	}))
	if out["alreadyTracked"] != true || len(out["granted"].([]any)) != 1 {
		t.Fatalf("out = %v", out)
	}
	if res := callTool(t, cs, "set_db_function_permission", map[string]any{
		"project_id": testProjectA, "function": "public.short_code", "role": "anon", "allowed": false,
	}); res.IsError {
		t.Fatalf("revoking a grant that never existed is already done: %s", resultText(res))
	}
}
