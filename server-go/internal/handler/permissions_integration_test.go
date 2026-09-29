//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// The engine contract against the real store: 404 for an unknown project,
// arrays never null, and what a write stores is what the document serves.
func TestPermissionDocument_AgainstPostgres(t *testing.T) {
	store := pgstore.New(t)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: permProjectID, OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	h := NewPermissionHandler(store.Permissions(), store, &fakeInspector{})
	router := chi.NewRouter()
	router.Route("/api/provision/{projectId}/permissions", h.PermissionRoutes)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	if w := call(http.MethodGet, "/api/provision/proj-unknown/permissions/", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404", w.Code)
	}
	w := call(http.MethodGet, permBase+"/permissions/", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) !=
		`{"projectId":"proj-1","version":0,"tables":[],"functions":[],"functionPermissions":[]}` {
		t.Fatalf("empty document = %d %s", w.Code, w.Body.String())
	}

	put := call(http.MethodPut, permSelectPath, `{"filter":{"owner_id":{"_eq":"X-Excalibase-User-Id"}},"columns":["id"],"limit":5}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d %s", put.Code, put.Body.String())
	}
	w = call(http.MethodGet, permBase+"/permissions/", "")
	var doc domain.PermissionDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || len(doc.Tables) != 1 || doc.Tables[0].Insert != nil {
		t.Fatalf("document %s", w.Body.String())
	}
	var served, written map[string]any
	_ = json.Unmarshal(doc.Tables[0].Select, &served)
	_ = json.Unmarshal(put.Body.Bytes(), &written)
	servedJSON, _ := json.Marshal(served)
	writtenJSON, _ := json.Marshal(written)
	if string(servedJSON) != string(writtenJSON) {
		t.Fatalf("served %s, written %s", servedJSON, writtenJSON)
	}
	if strings.Contains(w.Body.String(), `"insert"`) || strings.Contains(w.Body.String(), "null") {
		t.Fatalf("absent operations must be left out, not null: %s", w.Body.String())
	}
}
