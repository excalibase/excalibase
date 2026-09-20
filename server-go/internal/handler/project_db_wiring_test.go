package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// A handler with no resolver applies no schema and cannot answer
// schema/apply — boot needs to be able to see that.
func TestHasProjectDB_ReportsWhetherAResolverIsWired(t *testing.T) {
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, nil, nil, "")
	if h.HasProjectDB() {
		t.Fatal("a handler with no resolver reported one")
	}
	h.SetProjectDBFn(func(context.Context, string) (*sql.DB, error) { return nil, nil })
	if !h.HasProjectDB() {
		t.Fatal("a wired resolver was not reported")
	}
}

func TestAutoMigrates_ReportsTheConfiguredChoice(t *testing.T) {
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, nil, nil, "")
	h.SetAutoMigrate(true)
	if !h.AutoMigrates() {
		t.Error("auto-migrate on was reported off")
	}
	h.SetAutoMigrate(false)
	if h.AutoMigrates() {
		t.Error("auto-migrate off was reported on")
	}
}

// With a resolver wired, schema/apply stops answering "not configured".
func TestApplySchemaFromStore_StopsAnswering503WhenWired(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	h := NewFunctionHandler(store, nil, nil, nil, nil, "")
	h.SetProjectDBFn(func(context.Context, string) (*sql.DB, error) { return nil, nil })

	req := httptest.NewRequest("POST", "/api/projects/proj_a/schema/apply", nil)
	w := httptest.NewRecorder()
	applySchemaRouter(h).ServeHTTP(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("schema/apply still reports no resolver: %s", w.Body.String())
	}
}
