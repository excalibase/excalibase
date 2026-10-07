package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testPublishableKey = "esk_pub_live_abc123" //gitleaks:allow fabricated test key

// fakeDataPlane is the project's data API: the auth token exchange and the
// REST and GraphQL endpoints, recording what reached it.
type fakeDataPlane struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	reply    func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeDataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	if r.URL.Path == "/auth/acme/"+testProjectA+"/token" && r.Method == http.MethodPost {
		var exchange map[string]string
		if json.Unmarshal(body, &exchange) != nil || exchange["grant_type"] != "api_key" || exchange["api_key"] != testPublishableKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"accessToken":"anon-jwt","expiresIn":3600}`))
		return
	}
	f.reply(w, r)
}

func probeSession(t *testing.T, plane *fakeDataPlane, caller Caller) (*mcp.ClientSession, *fakeRoutes) {
	t.Helper()
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
	settings := testSettings
	settings.DataPlaneURL = server.URL
	return sessionWith(t, routes, &recordingAudit{}, caller, settings), routes
}

func TestProbeRunsOneRequestAsTheAnonRole(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"permission_denied","message":"Counting rows of public.todos is not permitted"}`))
	}}
	cs, routes := probeSession(t, plane, readOnlyCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey,
		"path": "todos", "query": "select=id,title&order=created_at.desc&limit=20", "prefer": "count=exact",
		"origin": "https://todo-proj-a.apps.example.test",
	}))
	if out["status"] != float64(403) || !strings.Contains(out["body"].(string), "Counting rows") {
		t.Fatalf("out = %v", out)
	}
	if len(routes.calls) != 1 || callKey(routes.calls[0]) != "GET "+projectsA+"/info/" {
		t.Fatalf("project access must be checked through the router first: %+v", routes.calls)
	}
	if len(plane.requests) != 4 {
		t.Fatalf("want the token exchange, one request and two preflights, got %d", len(plane.requests))
	}
	request := plane.requests[1]
	if request.Method != http.MethodGet || request.URL.Path != "/"+testProjectA+"/api/v1/todos" ||
		request.URL.Query().Get("order") != "created_at.desc" || request.URL.Query().Get("limit") != "20" {
		t.Errorf("request = %s %s", request.Method, request.URL)
	}
	for header, want := range map[string]string{
		"Authorization": "Bearer anon-jwt", "X-Excalibase-Publishable-Key": testPublishableKey,
		"Prefer": "count=exact", "Origin": "https://todo-proj-a.apps.example.test",
	} {
		if got := request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestProbeRefusesWhatCouldReachAnythingElse(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }}
	cs, _ := probeSession(t, plane, writeCaller())
	for name, args := range map[string]map[string]any{
		"a secret key":       {"publishable_key": "esk_sec_live_abc", "path": "todos"},
		"a climbing path":    {"publishable_key": testPublishableKey, "path": "../admin"},
		"a nested path":      {"publishable_key": testPublishableKey, "path": "todos/../../auth"},
		"an absolute URL":    {"publishable_key": testPublishableKey, "path": "http://evil.example/x"},
		"an unknown method":  {"publishable_key": testPublishableKey, "path": "todos", "method": "TRACE"},
		"a header injection": {"publishable_key": testPublishableKey, "path": "todos", "prefer": "count=exact\r\nX-Evil: 1"},
	} {
		args["project_id"] = testProjectA
		if res := callTool(t, cs, "test_api_request", args); !res.IsError {
			t.Errorf("%s was not refused", name)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("a refused probe reached the data plane: %d requests", len(plane.requests))
	}
}

func TestProbeOnAReadOnlyConnectionOnlyReads(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }}
	cs, _ := probeSession(t, plane, readOnlyCaller())
	for name, args := range map[string]map[string]any{
		"a REST write":          {"path": "todos", "method": "POST", "body": `{"title":"x"}`},
		"a GraphQL mutation":    {"api": "graphql", "body": `{"query":"mutation { createTodos(input: {title: \"x\"}) { id } }"}`},
		"an escaped mutation":   {"api": "graphql", "body": `{"query":"\u006dutation { deleteTodos(where: {}) { id } }"}`},
		"a key in another case": {"api": "graphql", "body": `{"query":"mutation { deleteTodos(where: {}) { id } }","Query":"{ todos { id } }"}`},
		"a batch":               {"api": "graphql", "body": `[{"query":"mutation { deleteTodos(where: {}) { id } }"}]`},
	} {
		args["project_id"], args["publishable_key"] = testProjectA, testPublishableKey
		if res := callTool(t, cs, "test_api_request", args); !res.IsError {
			t.Errorf("%s ran on a read-only connection", name)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("requests = %d", len(plane.requests))
	}
}

func TestProbeWritesOnAReadWriteConnection(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":[{"id":1}]}`))
	}}
	cs, _ := probeSession(t, plane, writeCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey,
		"path": "todos", "method": "POST", "body": `{"title":"probe"}`, "prefer": "return=representation",
	}))
	if out["status"] != float64(201) || plane.bodies[1] != `{"title":"probe"}` || plane.requests[1].Header.Get("Content-Type") != "application/json" {
		t.Fatalf("out = %v body = %q", out, plane.bodies[1])
	}
}

func TestProbeGraphQL(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+testProjectA+"/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"todos":[]}}`))
	}}
	cs, _ := probeSession(t, plane, readOnlyCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey, "api": "graphql", "body": `{"query":"{ todos { id } }"}`,
	}))
	if out["status"] != float64(200) {
		t.Fatalf("out = %v", out)
	}
}

func TestProbeNeverFollowsARedirectAndTruncatesTheBody(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+testProjectA+"/api/v1/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 3*maxProbeBody)))
	}}
	cs, _ := probeSession(t, plane, readOnlyCaller())
	redirect := structured(t, callTool(t, cs, "test_api_request", map[string]any{"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "redirect"}))
	headers, _ := redirect["headers"].(map[string]any)
	if redirect["status"] != float64(http.StatusFound) || len(plane.requests) != 2 || headers["Location"] != nil {
		t.Fatalf("redirect = %v, requests = %d", redirect, len(plane.requests))
	}
	big := structured(t, callTool(t, cs, "test_api_request", map[string]any{"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos"}))
	if len(big["body"].(string)) != maxProbeBody || big["truncated"] != true {
		t.Fatalf("body length %d truncated %v", len(big["body"].(string)), big["truncated"])
	}
}

func TestProbeFailsWhenTheKeyIsRefused(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }}
	cs, _ := probeSession(t, plane, readOnlyCaller())
	res := callTool(t, cs, "test_api_request", map[string]any{"project_id": testProjectA, "publishable_key": "esk_pub_live_wrong", "path": "todos"})
	if !res.IsError || !strings.Contains(resultText(res), "publishable key") {
		t.Fatalf("res = %s", resultText(res))
	}
}

// corsPlane answers preflights as the data plane would for an allowlist of one origin.
func corsPlane(allowed string) *fakeDataPlane {
	return corsPlaneAllowing(allowed, "Authorization, Content-Type, Prefer, X-Excalibase-Publishable-Key")
}

func corsPlaneAllowing(allowed, headers string) *fakeDataPlane {
	return &fakeDataPlane{reply: func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == allowed {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", headers)
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}}
}

func TestProbeChecksTheSignInPreflightFromTheGivenOrigin(t *testing.T) {
	for origin, wantBlocked := range map[string]bool{"https://web.example.test": false, "http://localhost:5173": true} {
		plane := corsPlane("https://web.example.test")
		cs, _ := probeSession(t, plane, writeCaller())
		out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
			"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos", "origin": origin,
		}))
		cors, _ := out["cors"].(map[string]any)
		signIn, _ := cors["signIn"].(map[string]any)
		if signIn["request"] != "OPTIONS /auth/acme/"+testProjectA+"/token" || signIn["allowed"] == wantBlocked {
			t.Errorf("%s: signIn = %v", origin, signIn)
		}
		if blocked, _ := cors["blocked"].(string); wantBlocked != strings.Contains(blocked, "add_cors_origin") {
			t.Errorf("%s: cors = %v", origin, cors)
		}
		preflight := plane.requests[2]
		if preflight.Method != http.MethodOptions || preflight.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
			preflight.Header.Get("Origin") != origin {
			t.Errorf("%s: sign-in preflight = %s %s %v", origin, preflight.Method, preflight.URL, preflight.Header)
		}
		request := plane.requests[3]
		if request.URL.Path != "/"+testProjectA+"/api/v1/todos" ||
			request.Header.Get("Access-Control-Request-Headers") != "authorization,x-excalibase-publishable-key" {
			t.Errorf("%s: request preflight = %s %v", origin, request.URL, request.Header)
		}
	}
}

func TestProbeRunsAsASignedInEndUser(t *testing.T) {
	userToken := fakeJWT(`{"projectId":"proj-a","role":"user","userId":"42"}`)
	plane := corsPlane("")
	cs, _ := probeSession(t, plane, writeCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "access_token": userToken, "method": "POST", "path": "rpc/short_code", "body": `{"url":"https://x.test"}`,
	}))
	if out["status"] != float64(200) || len(plane.requests) != 1 {
		t.Fatalf("out %v, %d requests", out, len(plane.requests))
	}
	request := plane.requests[0]
	if request.URL.Path != "/"+testProjectA+"/api/v1/rpc/short_code" || request.Header.Get("Authorization") != "Bearer "+userToken ||
		request.Header.Get("X-Excalibase-Publishable-Key") != "" {
		t.Errorf("request = %s %v", request.URL, request.Header)
	}
}

func TestProbeForwardsOnlyAnEndUsersToken(t *testing.T) {
	plane := corsPlane("")
	cs, _ := probeSession(t, plane, writeCaller())
	for name, args := range map[string]map[string]any{
		"no credential":         {"path": "todos"},
		"a personal token":      {"access_token": "excali_" + strings.Repeat("ab", 8), "path": "todos"},
		"a secret key":          {"access_token": "esk_sec_live_abc", "path": "todos"},
		"a token with newlines": {"access_token": "a.b.c\r\nX-Evil: 1", "path": "todos"},
		"another project's":     {"access_token": fakeJWT(`{"projectId":"proj-b","role":"user"}`), "path": "todos"},
		"a service token":       {"access_token": fakeJWT(`{"projectId":"proj-a","role":"service"}`), "path": "todos"},
		"a token with no role":  {"access_token": fakeJWT(`{"projectId":"proj-a"}`), "path": "todos"},
		"a deeper rpc path":     {"publishable_key": testPublishableKey, "path": "rpc/a/b"},
	} {
		args["project_id"] = testProjectA
		if res := callTool(t, cs, "test_api_request", args); !res.IsError {
			t.Errorf("%s was not refused", name)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("a refused probe reached the data plane: %d requests", len(plane.requests))
	}
}

// fakeJWT is an unsigned token with these claims: the probe reads claims, the engine checks signatures.
func fakeJWT(claims string) string {
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
}

func TestProbeSaysWhenThePreflightLeavesOutAHeader(t *testing.T) {
	plane := corsPlaneAllowing("https://web.example.test", "*")
	cs, _ := probeSession(t, plane, writeCaller())
	out := structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos", "prefer": "count=exact",
		"origin": "https://web.example.test",
	}))
	cors, _ := out["cors"].(map[string]any)
	request, _ := cors["request"].(map[string]any)
	if request["allowed"] != false || !strings.Contains(request["error"].(string), "authorization") || cors["blocked"] == nil {
		t.Fatalf("a wildcard does not cover Authorization: %v", cors)
	}
}
