package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeWithNoCredentialRunsAsAnonWithoutSigningIn(t *testing.T) {
	plane := corsPlane("")
	cs, _ := probeSession(t, plane, readOnlyCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{"project_id": testProjectA, "path": "todos"}))
	if out["status"] != float64(200) || len(plane.requests) != 1 {
		t.Fatalf("out %v, %d requests", out, len(plane.requests))
	}
	request := plane.requests[0]
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-Excalibase-Publishable-Key") != "" {
		t.Errorf("an anonymous check carries no credential: %v", request.Header)
	}
	if as, _ := out["as"].(string); !strings.Contains(as, "anon") {
		t.Errorf("the answer must say it ran as anon: %v", out)
	}
}

func TestAnonProbePreflightAsksForNoAuthorizationHeaderAndSkipsSignIn(t *testing.T) {
	plane := corsPlane("https://web.example.test")
	cs, _ := probeSession(t, plane, writeCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "path": "todos", "origin": "https://web.example.test",
	}))
	cors, _ := out["cors"].(map[string]any)
	if cors["signIn"] != nil || cors["blocked"] != nil {
		t.Fatalf("cors = %v", cors)
	}
	preflight := plane.requests[1]
	if preflight.Method != http.MethodOptions || strings.Contains(preflight.Header.Get("Access-Control-Request-Headers"), "authorization") {
		t.Errorf("preflight = %s %v", preflight.Method, preflight.Header)
	}
}

func corsListSession(t *testing.T, listBody string) (map[string]any, *fakeDataPlane) {
	t.Helper()
	plane := corsPlane("https://other.example.test")
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
	routes.on(http.MethodGet, projectsA+"/cors/", 200, listBody)
	settings := testSettings
	settings.DataPlaneURL = server.URL
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
	return structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos", "origin": "https://web.example.test",
	})), plane
}

func TestBlockedOriginAlreadyListedSaysWaitInsteadOfAdd(t *testing.T) {
	out, _ := corsListSession(t, `{"allowedOrigins":["https://web.example.test"],"allowWildcard":false}`)
	cors, _ := out["cors"].(map[string]any)
	blocked, _ := cors["blocked"].(string)
	if !strings.Contains(blocked, "already listed") || !strings.Contains(blocked, "1 minute") || strings.Contains(blocked, "Add the origin") {
		t.Fatalf("blocked = %q", blocked)
	}
}

func TestBlockedOriginNotListedStillSaysAdd(t *testing.T) {
	out, _ := corsListSession(t, `{"allowedOrigins":["https://elsewhere.example.test"],"allowWildcard":false}`)
	cors, _ := out["cors"].(map[string]any)
	if blocked, _ := cors["blocked"].(string); !strings.Contains(blocked, "add_cors_origin") || !strings.Contains(blocked, "1 minute") {
		t.Fatalf("blocked = %q", blocked)
	}
}
