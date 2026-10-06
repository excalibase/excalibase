package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
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

type failingScheduleStore struct{}

func (failingScheduleStore) UpsertSchedule(context.Context, *domain.BackupSchedule) error {
	return errors.New("dial tcp 10.0.0.9:5432: connection refused")
}
func (failingScheduleStore) ListEnabledSchedules(context.Context) ([]domain.BackupSchedule, error) {
	return nil, nil
}
func (failingScheduleStore) DeleteSchedule(context.Context, string) error { return nil }

// A valid schedule that cannot be stored is our failure, told without detail.
func TestBackupHandler_UpsertSchedule_StoreFailureIsOurs(t *testing.T) {
	h := NewBackupHandler(nil)
	h.SetScheduler(service.NewBackupScheduler(service.BackupSchedulerConfig{Schedules: failingScheduleStore{}}))
	r := chi.NewRouter()
	r.Post("/api/backup/{projectId}/schedule", h.UpsertSchedule)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/backup/proj-b/schedule", bytes.NewReader([]byte(`{"cron":"0 2 * * *","enabled":true}`))))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "10.0.0.9") {
		t.Errorf("got %d %s, want 500 without the cause", w.Code, w.Body.String())
	}
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

	// A schedule outside the one format every route takes → 400, with the rule.
	for _, cron := range []string{"0 0 2 * * *", "*/5 * * * *", "@every 1m"} {
		req = httptest.NewRequest("POST", "/api/backup/proj-b/schedule", bytes.NewReader([]byte(`{"cron":"`+cron+`","enabled":true}`)))
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "backup schedule") {
			t.Errorf("%q: got %d %s, want 400 naming the rule", cron, w.Code, w.Body.String())
		}
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
