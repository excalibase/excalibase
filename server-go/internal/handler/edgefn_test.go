package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
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
			var body struct{ ID, Code string }
			json.NewDecoder(r.Body).Decode(&body)
			scripts[body.ID] = body.Code
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/invoke/") {
			id := r.URL.Path[8:]
			if _, ok := scripts[id]; !ok {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"result": "ok"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/delete/") && r.Method == "DELETE" {
			id := r.URL.Path[8:]
			delete(scripts, id)
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			return
		}
		w.WriteHeader(404)
	}))
}

func setupEdgeFnRouter(t *testing.T) (chi.Router, *edgefn.ScriptStore) {
	t.Helper()
	dir := t.TempDir()
	store := edgefn.NewScriptStore(dir)
	srv := mockDenoServer()
	t.Cleanup(srv.Close)
	client := edgefn.NewRuntimeClient(srv.URL)

	h := NewEdgeFnHandler(store, client)

	r := chi.NewRouter()
	r.Route("/api/functions", func(r chi.Router) {
		h.Routes(r)
	})
	return r, store
}

func TestEdgeFnListEmpty(t *testing.T) {
	r, _ := setupEdgeFnRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/functions", nil))

	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
	var result []*edgefn.Script
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected 0, got %d", len(result))
	}
}

func TestEdgeFnCreateAndGet(t *testing.T) {
	r, _ := setupEdgeFnRouter(t)

	// Create
	body := `{"id":"hello","name":"hello","code":"function handler(d){return d;}","hookType":"custom"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/functions", strings.NewReader(body)))

	if w.Code != 201 {
		t.Errorf("create status: %d, body: %s", w.Code, w.Body.String())
	}

	// List
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/functions", nil))
	var scripts []*edgefn.Script
	json.NewDecoder(w.Body).Decode(&scripts)
	if len(scripts) != 1 {
		t.Errorf("expected 1, got %d", len(scripts))
	}

	// Get
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/functions/hello", nil))
	if w.Code != 200 {
		t.Errorf("get status: %d", w.Code)
	}
}

func TestEdgeFnInvoke(t *testing.T) {
	r, store := setupEdgeFnRouter(t)
	store.Save(&edgefn.Script{ID: "inv-fn", Name: "test", Code: "function handler(d){return d;}", HookType: "custom", Active: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/functions/inv-fn/invoke", strings.NewReader(`{"key":"val"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("invoke status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestEdgeFnDelete(t *testing.T) {
	r, store := setupEdgeFnRouter(t)
	store.Save(&edgefn.Script{ID: "del-fn", Name: "del", Code: "code", Active: true})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/functions/del-fn", nil))

	if w.Code != 200 {
		t.Errorf("delete status: %d", w.Code)
	}

	got, _ := store.Get("del-fn")
	if got != nil {
		t.Error("should be deleted from store")
	}
}

func TestEdgeFnGetNotFound(t *testing.T) {
	r, _ := setupEdgeFnRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/functions/nope", nil))
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestEdgeFnRuntimeStatus(t *testing.T) {
	r, _ := setupEdgeFnRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/functions/runtime/status", nil))
	if w.Code != 200 {
		t.Errorf("runtime status: %d, body: %s", w.Code, w.Body.String())
	}
}
