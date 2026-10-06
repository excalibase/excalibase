package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// EXC-555: the JSON routes refuse an oversized body with 413 on the real
// router, before any handler reads it.
func TestJSONRoutesRefuseOversizedBodies(t *testing.T) {
	router, who := matrixRouter(t)
	dev := who[callerDeveloper]
	routes := []struct {
		method, path, token string
		limit               int64
	}{
		{"POST", "/api/auth/register", "", custommw.AuthBodyLimit},
		{"POST", "/api/auth/login", "", custommw.AuthBodyLimit},
		{"POST", "/api/provision", dev, custommw.DefaultBodyLimit},
		{"POST", "/api/orgs", dev, custommw.DefaultBodyLimit},
		{"POST", "/api/orgs/" + matrixOrgA + "/members", dev, custommw.DefaultBodyLimit},
		{"POST", "/api/schema/" + matrixProjectA + "/query", dev, custommw.SchemaBodyLimit},
		{"POST", "/api/schema/" + matrixProjectA + "/ddl", dev, custommw.SchemaBodyLimit},
		{"POST", "/api/schema/" + matrixProjectA + "/tables/t/rows", dev, custommw.SchemaBodyLimit},
		{"POST", "/api/projects/" + matrixProjectA + "/functions/secrets", dev, custommw.DefaultBodyLimit},
	}
	for _, route := range routes {
		body := bytes.Repeat([]byte("a"), int(route.limit)+1)
		req := httptest.NewRequest(route.method, route.path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if route.token != "" {
			req.Header.Set("Authorization", "Bearer "+route.token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s: got %d, want 413 (body=%.200s)", route.method, route.path, w.Code, w.Body.String())
		}
	}
}

// The table import keeps its own, larger limit: a body over the JSON cap is
// not refused by it.
func TestTableImportKeepsItsOwnLimit(t *testing.T) {
	router, who := matrixRouter(t)
	body := bytes.Repeat([]byte("a"), int(custommw.SchemaBodyLimit)+1)
	req := httptest.NewRequest("POST", "/api/schema/"+matrixProjectA+"/import", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+who[callerDeveloper])
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusRequestEntityTooLarge && bytes.Contains(w.Body.Bytes(), []byte("the limit is")) {
		t.Errorf("the JSON body cap was applied to the import route: %s", w.Body.String())
	}
}
