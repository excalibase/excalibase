package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A project without backups has nowhere a backup could go; the request is
// refused rather than answered with a backup that can only fail later.
func TestBackupHandler_Trigger_RefusedWithoutBackups(t *testing.T) {
	r, _ := setupBackupHandlerWithStore(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/provision/p1/backup/trigger", nil))

	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", w.Code, w.Body.String())
	}
}
