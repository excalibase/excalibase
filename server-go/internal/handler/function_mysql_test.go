package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// A function that declares tables on a MySQL project (ADR 0041) is refused
// with 409: declared schemas are applied over Postgres only.
func TestApplyExtractedSchema_MySQLProjectIsRefused(t *testing.T) {
	h := &FunctionHandler{
		store:       edgefn.NewFunctionStore(t.TempDir()),
		autoMigrate: true,
		projectDBFn: func(context.Context, string) (*sql.DB, error) {
			return nil, fmt.Errorf("open: %w", domain.ErrPostgresOnly)
		},
	}
	code := `function defineSchema(s) { globalThis.__excalibase_schema = { tables: {} }; } defineSchema({});`
	rolledBack := false
	w := httptest.NewRecorder()
	fn := &edgefn.Function{ProjectID: "proj_my", ID: "fn"}
	ok := h.applyExtractedSchema(w, httptest.NewRequest(http.MethodPost, "/", nil), "proj_my", code, fn, func() { rolledBack = true })
	if ok || !rolledBack || w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "declared schemas are not available for MySQL projects yet") {
		t.Errorf("ok=%v rolledBack=%v %d %s", ok, rolledBack, w.Code, w.Body.String())
	}
}
