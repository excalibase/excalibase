package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func okPlane() *fakeDataPlane {
	return &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }}
}

func probeTodos() map[string]any {
	return map[string]any{"project_id": testProjectA, "publishable_key": testPublishableKey, "path": "todos"}
}

func TestProbeStopsWhenTheProjectIsNotTheCallers(t *testing.T) {
	plane := okPlane()
	cs, routes := probeSession(t, plane, writeCaller())
	routes.on(http.MethodGet, projectsA+"/info/", 404, `{"error":"not found"}`)
	if res := callTool(t, cs, "test_api_request", probeTodos()); !res.IsError || len(plane.requests) != 0 {
		t.Fatalf("error %v, data plane requests %d", res.IsError, len(plane.requests))
	}
}

func TestProbeIsOffWithoutADataPlaneAddress(t *testing.T) {
	routes := newFakeRoutes()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "test_api_request", probeTodos())
	if !res.IsError || !strings.Contains(resultText(res), "not available") || len(routes.calls) != 0 {
		t.Fatalf("res = %s, calls = %+v", resultText(res), routes.calls)
	}
}

func TestProbeRefusesAProjectWithoutAnOrganization(t *testing.T) {
	plane := okPlane()
	cs, routes := probeSession(t, plane, writeCaller())
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a"}`)
	if res := callTool(t, cs, "test_api_request", probeTodos()); !res.IsError || len(plane.requests) != 0 {
		t.Fatalf("error %v, data plane requests %d", res.IsError, len(plane.requests))
	}
}

func TestProbeRefusesMalformedInput(t *testing.T) {
	plane := okPlane()
	cs, _ := probeSession(t, plane, writeCaller())
	for name, args := range map[string]map[string]any{
		"a body that is not JSON":   {"path": "todos", "method": "POST", "body": "title=x"},
		"an oversized body":         {"path": "todos", "method": "POST", "body": `"` + strings.Repeat("x", maxProbeRequestBody) + `"`},
		"a GET with a body":         {"path": "todos", "body": `{"title":"x"}`},
		"an origin with a path":     {"path": "todos", "origin": "https://todo.example.test/app"},
		"an origin with a user":     {"path": "todos", "origin": "https://u:p@todo.example.test"},
		"an origin with a query":    {"path": "todos", "origin": "https://todo.example.test?x=1"},
		"an oversized query":        {"path": "todos", "query": "select=" + strings.Repeat("a", maxProbeQuery)},
		"an oversized prefer":       {"path": "todos", "prefer": strings.Repeat("a", maxProbeHeader+1)},
		"a GraphQL extensions key":  {"api": "graphql", "body": `{"query":"{ todos { id } }","extensions":{"persistedQuery":{}}}`},
		"an empty GraphQL document": {"api": "graphql", "body": `{"variables":{}}`},
		"an unknown api":            {"api": "soap", "path": "todos"},
	} {
		args["project_id"], args["publishable_key"] = testProjectA, testPublishableKey
		if res := callTool(t, cs, "test_api_request", args); !res.IsError {
			t.Errorf("%s was not refused", name)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("a refused probe reached the data plane: %d requests", len(plane.requests))
	}
}

func TestProbeRefusesAMutationBesideAQuery(t *testing.T) {
	plane := okPlane()
	cs, _ := probeSession(t, plane, readOnlyCaller())
	for _, document := range []string{
		`{"query":"query A { todos { id } } mutation B { deleteTodos(where: {}) { id } }","operationName":"B"}`,
		`{"query":"{todos{id}}mutation{deleteTodos(where: {}){id}}"}`,
	} {
		args := map[string]any{"project_id": testProjectA, "publishable_key": testPublishableKey, "api": "graphql", "body": document}
		if res := callTool(t, cs, "test_api_request", args); !res.IsError {
			t.Errorf("%s ran on a read-only connection", document)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("requests = %d", len(plane.requests))
	}
}

func TestProbeSendsTheCheckedGraphQLDocument(t *testing.T) {
	plane := &fakeDataPlane{reply: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"data":{}}`)) }}
	cs, _ := probeSession(t, plane, readOnlyCaller())
	structured(t, callTool(t, cs, "test_api_request", map[string]any{
		"project_id": testProjectA, "publishable_key": testPublishableKey, "api": "graphql",
		"body": `{"query":"query Todos($done: Boolean) { todos { id } }", "variables": {"done": false}}`,
	}))
	var sent map[string]any
	if err := json.Unmarshal([]byte(plane.bodies[1]), &sent); err != nil {
		t.Fatal(err)
	}
	variables, _ := sent["variables"].(map[string]any)
	if !strings.HasPrefix(sent["query"].(string), "query Todos") || variables["done"] != false || len(sent) != 2 {
		t.Fatalf("sent %s", plane.bodies[1])
	}
}

func TestProbeSendsHeadAndAPlainOrigin(t *testing.T) {
	plane := okPlane()
	cs, _ := probeSession(t, plane, readOnlyCaller())
	args := probeTodos()
	args["method"], args["origin"] = "head", "https://todo.example.test/"
	out := structured(t, callTool(t, cs, "test_api_request", args))
	sent := plane.requests[1]
	if out["status"] != float64(200) || sent.Method != http.MethodHead || sent.Header.Get("Origin") != "https://todo.example.test" {
		t.Fatalf("out %v method %s origin %q", out, sent.Method, sent.Header.Get("Origin"))
	}
}

func TestProbesArePacedPerUser(t *testing.T) {
	limits := newProbeLimits()
	for i := 0; i < probeBurst; i++ {
		if !limits.allow("user-1") {
			t.Fatalf("probe %d refused within the burst", i+1)
		}
	}
	if limits.allow("user-1") {
		t.Fatal("a probe past the burst was allowed")
	}
	if !limits.allow("user-2") {
		t.Fatal("another user shares the first user's allowance")
	}
}

func TestProbePacingAnswersAsATooManyError(t *testing.T) {
	cs, _ := probeSession(t, okPlane(), readOnlyCaller())
	var last string
	for i := 0; i <= probeBurst; i++ {
		last = resultText(callTool(t, cs, "test_api_request", probeTodos()))
	}
	if !strings.Contains(last, "too many probes") {
		t.Fatalf("the call past the burst answered %s", last)
	}
}
