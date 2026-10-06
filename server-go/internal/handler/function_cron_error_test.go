package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
)

// A schedule that could not be saved is told in words a developer can act on;
// the driver's text and our internal step names stay in the log.
func TestCronSyncFailureAnswer(t *testing.T) {
	busy := fmt.Errorf("ensure cron tables: %w", &pq.Error{Code: "55P03", Message: "canceling statement due to lock timeout"})
	msg, code := cronSyncFailure(busy)
	if code != http.StatusConflict {
		t.Errorf("lock timeout: status %d, want 409", code)
	}
	if !strings.Contains(msg, "another deploy") {
		t.Errorf("lock timeout: %q should say another deploy is running", msg)
	}

	msg, code = cronSyncFailure(errors.New("ensure cron tables: pq: connection refused"))
	if code != http.StatusBadGateway {
		t.Errorf("other failure: status %d, want 502", code)
	}
	for _, leak := range []string{"pq", "ensure cron tables", "connection refused"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message %q leaks %q", msg, leak)
		}
	}
}

// A deploy whose schedule cannot be saved answers in plain words and leaves
// no half-created function behind.
func TestCreateFunction_CronSyncFailureIsPlainAndRollsBack(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	var orgStore storage.OrgStore
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), edgefn.NewRuntimeClient("http://127.0.0.1:1", ""),
		instStore, orgStore, "https://api.test.io")
	h.projectDBFn = func(context.Context, string) (*sql.DB, error) {
		return nil, errors.New("dial tcp 10.0.0.7:5432: connection refused")
	}
	router := chi.NewRouter()
	router.Post("/api/projects/{projectId}/functions", h.Create)

	body := `{"id":"hello","name":"Hello","files":[{"path":"index.ts","content":"export default () => new Response('ok')"}]}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/proj_p1/functions", strings.NewReader(body)))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	for _, leak := range []string{"open project db", "10.0.0.7", "connection refused"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("answer %q leaks %q", w.Body.String(), leak)
		}
	}
	if fn, err := store.Get("proj_p1", "hello"); err == nil && fn != nil {
		t.Errorf("function was kept after the failed deploy")
	}
}
