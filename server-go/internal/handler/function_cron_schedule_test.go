package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// A bundle whose cron schedule would never run as written is refused at
// deploy with 400 and a reason, and nothing is saved.
func TestCreateFunction_RefusesACronScheduleThatWouldNeverRun(t *testing.T) {
	for schedule, reason := range map[string]string{
		`{kind:"daily",hourUTC:25,minuteUTC:0}`:   "hourUTC",
		`{kind:"cron",expression:"0 0 0 * * *"}`:  "five fields",
		`{kind:"cron",expression:"@fortnightly"}`: "@daily",
	} {
		store := edgefn.NewFunctionStore(t.TempDir())
		instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
			"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
		}}
		var orgStore storage.OrgStore
		h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), edgefn.NewRuntimeClient("http://127.0.0.1:1", ""),
			instStore, orgStore, "https://api.test.io")
		router := chi.NewRouter()
		router.Post("/api/projects/{projectId}/functions", h.Create)

		code := fmt.Sprintf(`globalThis.__excalibase_crons = [{name:"nightly",schedule:%s,fnRef:{moduleName:"m",exportName:"x"},args:{}}];
export default () => new Response('ok')`, schedule)
		body, _ := json.Marshal(map[string]any{
			"id": "hello", "name": "Hello",
			"files": []map[string]string{{"path": "index.ts", "content": code}},
		})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/proj_p1/functions", strings.NewReader(string(body))))

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", schedule, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), reason) {
			t.Errorf("%s: answer %q should mention %q", schedule, w.Body.String(), reason)
		}
		if fn, err := store.Get("proj_p1", "hello"); err == nil && fn != nil {
			t.Errorf("%s: function was saved despite the refused schedule", schedule)
		}
	}
}

// A schedule the sync itself refuses is the caller's mistake, not ours.
func TestCronSyncFailure_InvalidScheduleIsABadRequest(t *testing.T) {
	err := fmt.Errorf("sync cron jobs: cron job %q: %w: hourUTC must be a whole number from 0 to 23", "nightly", scheduler.ErrInvalidSchedule)
	msg, code := cronSyncFailure(err)
	if code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
	if !strings.Contains(msg, "hourUTC") || strings.Contains(msg, "sync cron jobs") {
		t.Errorf("message %q should give the reason without the step name", msg)
	}
}
