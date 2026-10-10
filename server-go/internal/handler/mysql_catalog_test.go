package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/go-chi/chi/v5"
)

// ADR 0041: the create form offers the catalogue's MySQL versions, newest
// first, and never sees an image reference.
func TestMySQLCatalog_ListsVersionsWithoutImages(t *testing.T) {
	r := chi.NewRouter()
	NewMySQLCatalogHandler().Routes(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		DefaultVersion string `json:"defaultVersion"`
		Versions       []map[string]interface{}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DefaultVersion != config.DefaultMySQLVersion() {
		t.Errorf("default %q", body.DefaultVersion)
	}
	if len(body.Versions) != len(config.MySQLVersions()) {
		t.Fatalf("versions %v", body.Versions)
	}
	for i, version := range body.Versions {
		if version["version"] != config.MySQLVersions()[i] {
			t.Errorf("version %d = %v", i, version["version"])
		}
		if _, leaked := version["image"]; leaked {
			t.Errorf("the image reference reached the client: %v", version)
		}
	}
}
