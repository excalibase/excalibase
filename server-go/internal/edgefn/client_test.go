package edgefn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockDenoServer stands in for the Deno runtime. Speaks the new protocol:
//   - POST /deploy      { id, code, secrets } → 201
//   - POST /invoke/{id} InvokeRequest → InvokeResponse
//   - DELETE /delete/{id}
//   - GET /health
func mockDenoServer() *httptest.Server {
	scripts := make(map[string]DeployRequest)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/health" {
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "healthy", "scripts": len(scripts)})
			return
		}

		if r.URL.Path == "/deploy" && r.Method == "POST" {
			var body DeployRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			scripts[body.ID] = body
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID, "url": "/invoke/" + body.ID})
			return
		}

		if len(r.URL.Path) > len("/invoke/") && r.URL.Path[:len("/invoke/")] == "/invoke/" && r.Method == "POST" {
			id := r.URL.Path[len("/invoke/"):]
			if _, ok := scripts[id]; !ok {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			// Echo back a Response-shaped payload
			json.NewEncoder(w).Encode(InvokeResponse{
				Status:  200,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:    `{"ok":true}`,
			})
			return
		}

		if len(r.URL.Path) > len("/delete/") && r.URL.Path[:len("/delete/")] == "/delete/" && r.Method == "DELETE" {
			id := r.URL.Path[len("/delete/"):]
			delete(scripts, id)
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
			return
		}

		w.WriteHeader(404)
	}))
}

func deployRequest(id, code string) DeployRequest {
	return DeployRequest{ID: id, Code: code, Secrets: map[string]string{}}
}

func invokeRequest(body string) InvokeRequest {
	return InvokeRequest{
		Method:  "POST",
		URL:     "https://api.excalibase.io/functions/v1/test",
		Headers: map[string]string{"Content-Type": "application/json"},
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
	if err := client.Deploy(context.Background(), deployRequest("test-fn", "globalThis.__excalibase_default = () => new Response('ok')")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}

func TestRuntimeClientInvoke(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	client.Deploy(context.Background(), deployRequest("test-fn", "code"))

	result, err := client.Invoke(context.Background(), "test-fn", invokeRequest(`{"key":"val"}`))
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
		w.Header().Set("Content-Type", "application/json")
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
		w.Header().Set("Content-Type", "application/json")
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
