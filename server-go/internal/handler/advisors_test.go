package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/advisors"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/go-chi/chi/v5"
)

type fakeAdvisors struct {
	report  *advisors.Report
	err     error
	project string
}

func (f *fakeAdvisors) Run(_ context.Context, projectID string) (*advisors.Report, error) {
	f.project = projectID
	return f.report, f.err
}

func serveAdvisors(h *AdvisorsHandler) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/provision/{projectId}/advisors", h.Get)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/proj-1/advisors", nil))
	return w
}

func TestAdvisorsAnswersTheReport(t *testing.T) {
	runner := &fakeAdvisors{report: &advisors.Report{Engine: advisors.EngineMySQL, Findings: []advisors.Finding{
		{Check: advisors.CheckNoPrimaryKey, Category: advisors.CategoryPerformance, Level: advisors.LevelWarning, Object: "app.logs", Message: "m", FixHint: "h"},
	}, Checks: []advisors.CheckStatus{{ID: advisors.CheckNoPrimaryKey, Category: advisors.CategoryPerformance, Status: advisors.StatusRan}}}}
	w := serveAdvisors(NewAdvisorsHandler(runner))
	if w.Code != http.StatusOK || runner.project != "proj-1" {
		t.Fatalf("status %d project %q: %s", w.Code, runner.project, w.Body.String())
	}
	var body struct {
		Engine   string           `json:"engine"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Engine != "mysql" || len(body.Findings) != 1 || body.Findings[0]["check"] != "table_without_primary_key" || body.Findings[0]["fixHint"] != "h" {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestAdvisorsRefusesAProjectThatIsNotRunning(t *testing.T) {
	w := serveAdvisors(NewAdvisorsHandler(&fakeAdvisors{err: fmt.Errorf("open: %w", projectdb.ErrNotServable)}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// A failure says nothing about the database it came from.
func TestAdvisorsFailureHidesItsCause(t *testing.T) {
	w := serveAdvisors(NewAdvisorsHandler(&fakeAdvisors{err: errors.New(`pq: relation "secret_table" does not exist at 10.0.0.7`)}))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret_table") || strings.Contains(w.Body.String(), "10.0.0.7") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAdvisorsWithoutARunnerIsUnavailable(t *testing.T) {
	if w := serveAdvisors(NewAdvisorsHandler(nil)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
	var nilHandler *AdvisorsHandler
	if w := serveAdvisors(nilHandler); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil handler status %d", w.Code)
	}
}
