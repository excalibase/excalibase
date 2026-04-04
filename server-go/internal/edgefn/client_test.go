package edgefn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mockDenoServer() *httptest.Server {
	scripts := make(map[string]string)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/health" {
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "healthy", "scripts": len(scripts)})
			return
		}

		if r.URL.Path == "/deploy" && r.Method == "POST" {
			var body struct {
				ID   string `json:"id"`
				Code string `json:"code"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			scripts[body.ID] = body.Code
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID, "url": "/invoke/" + body.ID})
			return
		}

		if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/invoke/" {
			id := r.URL.Path[8:]
			if _, ok := scripts[id]; !ok {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"result": "ok", "scriptId": id})
			return
		}

		if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/delete/" && r.Method == "DELETE" {
			id := r.URL.Path[8:]
			delete(scripts, id)
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}

		if r.URL.Path == "/scripts" {
			json.NewEncoder(w).Encode(map[string]interface{}{"scripts": []string{}})
			return
		}

		w.WriteHeader(404)
	}))
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
	err := client.Deploy(context.Background(), "test-fn", "function handler(d) { return d; }")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}

func TestRuntimeClientInvoke(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	client.Deploy(context.Background(), "test-fn", "code")

	result, err := client.Invoke(context.Background(), "test-fn", map[string]string{"key": "val"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result == nil {
		t.Error("result should not be nil")
	}
}

func TestRuntimeClientInvokeNotFound(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	_, err := client.Invoke(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("expected error for missing function")
	}
}

func TestRuntimeClientDelete(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	client.Deploy(context.Background(), "del-fn", "code")

	err := client.Delete(context.Background(), "del-fn")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestRuntimeClientInvokeInvalidJSON(t *testing.T) {
	// Server returns invalid JSON — Invoke should return an error, not nil
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not valid json{{{"))
	}))
	defer srv.Close()

	client := NewRuntimeClient(srv.URL, "")
	_, err := client.Invoke(context.Background(), "test", nil)
	if err == nil {
		t.Error("Invoke with invalid JSON response should return error")
	}
}

func TestRuntimeClientListInvalidJSON(t *testing.T) {
	// Server returns invalid JSON — List should return an error, not nil
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

	// Deploy/Invoke should return errors but not panic
	err := client.Deploy(context.Background(), "x", "code")
	if err == nil {
		t.Error("expected error")
	}
}
