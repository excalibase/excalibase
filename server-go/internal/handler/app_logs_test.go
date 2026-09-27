package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/go-chi/chi/v5"
)

type fakeAppLogs struct {
	lines     []k8s.AppLogLine
	err       error
	asked     k8s.AppLogOptions
	truncated bool
	project   string
}

func (f *fakeAppLogs) Logs(_ context.Context, projectID, _ string, opts k8s.AppLogOptions) (k8s.AppLogPage, error) {
	f.project, f.asked = projectID, opts
	return k8s.AppLogPage{Lines: f.lines, Truncated: f.truncated}, f.err
}

func appLogsRequest(logs *fakeAppLogs, query string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/apps/{appId}/logs", NewAppLogHandler(logs).Logs)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+appTestProject+"/apps/app-1/logs"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAppLogHandler_ReturnsLinesAndACursor(t *testing.T) {
	at := time.Date(2026, 9, 27, 10, 0, 0, 123, time.UTC)
	logs := &fakeAppLogs{lines: []k8s.AppLogLine{{Pod: "web-1", Time: at, Text: "listening"}}}
	w := appLogsRequest(logs, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Lines  []k8s.AppLogLine `json:"lines"`
		Cursor string           `json:"cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 1 || body.Cursor != at.Format(time.RFC3339Nano) {
		t.Fatalf("body = %+v", body)
	}
	if logs.asked.TailLines != defaultAppLogTail || logs.asked.Since != nil || logs.project != appTestProject {
		t.Fatalf("asked = %+v for %s", logs.asked, logs.project)
	}
}

func TestAppLogHandler_PassesTheQuery(t *testing.T) {
	logs := &fakeAppLogs{}
	w := appLogsRequest(logs, "?since=2026-09-27T10:00:00.5Z&tail=20&previous=true")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if logs.asked.Since == nil || logs.asked.TailLines != 20 || !logs.asked.Previous {
		t.Fatalf("asked = %+v", logs.asked)
	}
	var body struct {
		Lines  []k8s.AppLogLine `json:"lines"`
		Cursor string           `json:"cursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Lines == nil || body.Cursor != "2026-09-27T10:00:00.5Z" {
		t.Fatalf("no new lines must keep the cursor where it was: %s", w.Body.String())
	}
}

func TestAppLogHandler_Refusals(t *testing.T) {
	for query, code := range map[string]int{
		"?since=yesterday": http.StatusBadRequest,
		"?tail=0":          http.StatusBadRequest,
		"?tail=5000":       http.StatusBadRequest,
		"?previous=maybe":  http.StatusBadRequest,
	} {
		if w := appLogsRequest(&fakeAppLogs{}, query); w.Code != code {
			t.Errorf("%s: status %d, want %d", query, w.Code, code)
		}
	}
	if w := appLogsRequest(&fakeAppLogs{err: apphost.ErrAppNotFound}, ""); w.Code != http.StatusNotFound {
		t.Errorf("missing app: %d", w.Code)
	}
	if w := appLogsRequest(&fakeAppLogs{err: errors.New("pq: down")}, ""); w.Code != http.StatusInternalServerError {
		t.Errorf("failure: %d", w.Code)
	}
}

func TestAppLogHandler_SaysWhenMoreFollow(t *testing.T) {
	logs := &fakeAppLogs{lines: []k8s.AppLogLine{{Pod: "p", Time: time.Now(), Text: "x"}}, truncated: true}
	w := appLogsRequest(logs, "?since=2026-09-27T10:00:00Z")
	if !strings.Contains(w.Body.String(), `"truncated":true`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}
