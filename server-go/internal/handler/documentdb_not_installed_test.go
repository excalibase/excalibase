package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// EXC-394: creating a DocumentDB project where DocumentDB is not installed is
// the installation's state, not a malformed request: 409 with the reason.
func TestCreatingADocumentDBProjectWhereItIsNotInstalledIsAConflict(t *testing.T) {
	w := httptest.NewRecorder()
	if !writeProjectCreationError(w, fmt.Errorf("provision: %w", service.ErrDocumentDBNotInstalled)) {
		t.Fatal("the refusal was not answered")
	}
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "DocumentDB is not installed") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
