package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestBackupHandler_SetterAndGetter(t *testing.T) {
	h := NewBackupHandler(nil)
	if h.Service() != nil {
		t.Error("Service() should be nil when constructed with nil svc")
	}
	// SetScheduler / SetRestoreOrchestrator are nil-tolerant setters.
	h.SetScheduler(nil)
	h.SetRestoreOrchestrator(nil)
}

func TestBackupHandler_UpsertSchedule_Validation(t *testing.T) {
	h := NewBackupHandler(nil)
	r := chi.NewRouter()
	r.Route("/api/backup/{projectId}", func(r chi.Router) {
		r.Post("/schedule", h.UpsertSchedule)
		r.Delete("/schedule", h.DeleteSchedule)
	})

	// Bad JSON → 400.
	req := httptest.NewRequest("POST", "/api/backup/proj-b/schedule", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: got %d", w.Code)
	}

	// Missing cron → 400.
	req = httptest.NewRequest("POST", "/api/backup/proj-b/schedule", bytes.NewReader([]byte(`{"retentionDays":7}`)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing cron: got %d", w.Code)
	}

	// Valid body but nil scheduler → 503.
	req = httptest.NewRequest("POST", "/api/backup/proj-b/schedule", bytes.NewReader([]byte(`{"cron":"0 2 * * *","enabled":true}`)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil scheduler upsert: got %d", w.Code)
	}

	// DeleteSchedule with nil scheduler → 503.
	req = httptest.NewRequest("DELETE", "/api/backup/proj-b/schedule", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil scheduler delete: got %d", w.Code)
	}
}
