package edgefn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testAppJSON     = "application/json"
	testContentType = "Content-Type"
	testInvokePath  = "/invoke/"
	testDeletePath  = "/delete/"
	testFnID        = "test-fn"
)

// mockDenoServer stands in for the Deno runtime. Speaks the new protocol:
//   - POST /deploy      { id, code, secrets } → 201
//   - POST /invoke/{id} InvokeRequest → InvokeResponse
//   - DELETE /delete/{id}
//   - GET /health
func mockDenoServer() *httptest.Server {
	scripts := make(map[string]DeployRequest)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testContentType, testAppJSON)
		switch {
		case r.URL.Path == "/health":
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "healthy", "scripts": len(scripts)})
		case r.URL.Path == "/deploy" && r.Method == "POST":
			handleMockDeploy(w, r, scripts)
		case strings.HasPrefix(r.URL.Path, testInvokePath) && r.Method == "POST":
			handleMockInvoke(w, r.URL.Path, scripts)
		case strings.HasPrefix(r.URL.Path, testDeletePath) && r.Method == "DELETE":
			handleMockDelete(w, r.URL.Path, scripts)
		default:
			w.WriteHeader(404)
		}
	}))
}

func handleMockDeploy(w http.ResponseWriter, r *http.Request, scripts map[string]DeployRequest) {
	var body DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(400)
		return
	}
	scripts[body.ID] = body
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"id": body.ID, "url": testInvokePath + body.ID})
}

func handleMockInvoke(w http.ResponseWriter, path string, scripts map[string]DeployRequest) {
	id := path[len(testInvokePath):]
	if _, ok := scripts[id]; !ok {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	json.NewEncoder(w).Encode(InvokeResponse{
		Status:  200,
		Headers: map[string]string{testContentType: testAppJSON},
		Body:    `{"ok":true}`,
	})
}

func handleMockDelete(w http.ResponseWriter, path string, scripts map[string]DeployRequest) {
	id := path[len(testDeletePath):]
	delete(scripts, id)
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
}

func deployRequest(id, code string) DeployRequest {
	return DeployRequest{ID: id, Code: code, Secrets: map[string]string{}}
}

func invokeRequest(body string) InvokeRequest {
	return InvokeRequest{
		Method:  "POST",
		URL:     "https://api.excalibase.io/functions/v1/test",
		Headers: map[string]string{testContentType: testAppJSON},
		Body:    body,
	}
}

func TestRuntimeClientHealth(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	healthy, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !healthy {
		t.Error("should be healthy")
	}
}

func TestRuntimeClientDeploy(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	if err := client.Deploy(context.Background(), deployRequest(testFnID, "globalThis.__excalibase_default = () => new Response('ok')")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}

func TestRuntimeClientInvoke(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	client.Deploy(context.Background(), deployRequest(testFnID, "code"))

	result, err := client.Invoke(context.Background(), testFnID, invokeRequest(`{"key":"val"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result == nil || result.Status != 200 {
		t.Errorf("result: %+v", result)
	}
	if result.Body != `{"ok":true}` {
		t.Errorf("body: %q", result.Body)
	}
}

func TestRuntimeClientInvokeNotFound(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	_, err := client.Invoke(context.Background(), "nonexistent", invokeRequest(""))
	if err == nil {
		t.Error("expected error for missing function")
	}
}

func TestRuntimeClientDelete(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	client.Deploy(context.Background(), deployRequest("del-fn", "code"))

	if err := client.Delete(context.Background(), "del-fn"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestRuntimeClientInvokeInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testContentType, testAppJSON)
		w.Write([]byte("not valid json{{{"))
	}))
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	_, err := client.Invoke(context.Background(), "test", invokeRequest(""))
	if err == nil {
		t.Error("Invoke with invalid JSON response should return error")
	}
}

func TestRuntimeClientListInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testContentType, testAppJSON)
		w.Write([]byte("broken json"))
	}))
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	_, err := client.List(context.Background())
	if err == nil {
		t.Error("List with invalid JSON response should return error")
	}
}

func TestRuntimeClientUnreachable(t *testing.T) {
	client := NewRuntimeClient("http://localhost:1", "") // nothing listening

	healthy, _ := client.Health(context.Background())
	if healthy {
		t.Error("should not be healthy when unreachable")
	}

	if err := client.Deploy(context.Background(), deployRequest("x", "code")); err == nil {
		t.Error("expected deploy error")
	}
}

// Logs endpoint coverage: success path, since-filter, 404, generic error,
// and invalid JSON response.
func TestRuntimeClientLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/logs/notfound" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/logs/oops" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/logs/badjson" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`not-json`))
			return
		}
		// /logs/hello — happy path. Honour `since` if present.
		_ = r.URL.Query().Get("since")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"logs": []map[string]interface{}{{"ts": 1, "level": "info", "msg": "ok"}},
		})
	}))
	defer srv.Close()
	client := NewRuntimeClient(srv.URL, "")
	ctx := context.Background()

	logs, err := client.Logs(ctx, "hello", 0)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}

	// since-filter path
	if _, err := client.Logs(ctx, "hello", 1234); err != nil {
		t.Errorf("Logs with since: %v", err)
	}

	// 404 → ErrLogsNotFound
	if _, err := client.Logs(ctx, "notfound", 0); err == nil {
		t.Error("expected ErrLogsNotFound")
	}

	// 500 → wrapped error
	if _, err := client.Logs(ctx, "oops", 0); err == nil {
		t.Error("expected error on 500")
	}

	// bad JSON
	if _, err := client.Logs(ctx, "badjson", 0); err == nil {
		t.Error("expected JSON decode error")
	}
}

func TestRuntimeClientLogs_Unreachable(t *testing.T) {
	c := NewRuntimeClient("http://localhost:1", "")
	if _, err := c.Logs(context.Background(), "any", 0); err == nil {
		t.Error("expected error on unreachable")
	}
}

func TestValidateProjectID(t *testing.T) {
	cases := []struct {
		name string
		pid  string
		ok   bool
	}{
		{"valid proj-style", "proj-abc123def0", true},
		{"valid default", "default", true},
		{"valid uuid-ish", "5dc7b84f9cbdeee67508af8090d97eca", true},
		{"empty rejected", "", false},
		{"slash rejected", "with/slash", false},
		{"dots rejected", "..", false},
		{"too long rejected", strings.Repeat("a", 65), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateProjectID(c.pid)
			if c.ok && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Errorf("expected error for %q", c.pid)
			}
		})
	}
}
