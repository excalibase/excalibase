package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// A relayed function response is marked so Studio can tell a function's own
// 401 from its session ending; the function cannot remove the mark.
func TestWriteRuntimeResponseMarksTheFunctionsAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRuntimeResponse(rec, &edgefn.InvokeResponse{
		Status:  http.StatusUnauthorized,
		Headers: map[string]string{custommw.FunctionResponseHeader: "0"},
		Body:    "nope",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want the function's 401", rec.Code)
	}
	if got := rec.Header().Get(custommw.FunctionResponseHeader); got != "1" {
		t.Fatalf("%s = %q, want 1", custommw.FunctionResponseHeader, got)
	}
}
