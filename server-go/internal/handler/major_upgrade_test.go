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

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type fakeMajorUpgrades struct {
	status   *service.MajorUpgradeStatus
	err      error
	req      domain.MajorUpgradeRequest
	actor    string
	statusOf string
}

func (f *fakeMajorUpgrades) Status(_ context.Context, projectID string) (*service.MajorUpgradeStatus, error) {
	f.statusOf = projectID
	return f.status, f.err
}

func (f *fakeMajorUpgrades) Start(_ context.Context, projectID string, req domain.MajorUpgradeRequest, actor string) (*domain.MajorUpgrade, error) {
	f.req, f.actor = req, actor
	if f.err != nil {
		return nil, f.err
	}
	return &domain.MajorUpgrade{ID: "mu-1", ProjectID: projectID, FromVersion: "16", ToVersion: req.TargetVersion,
		Status: domain.MajorUpgradeRunning, RequestedBy: actor}, nil
}

func majorUpgradeRouter(runs MajorUpgradeRunner, audit auditWriter) *chi.Mux {
	router := chi.NewRouter()
	router.Route("/api/provision/{projectId}/major-upgrade", NewMajorUpgradeHandler(runs, audit).Routes)
	return router
}

func callMajorUpgrade(router http.Handler, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(method, "/api/provision/p1/major-upgrade/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestMajorUpgradeStatusIsTheProjectsOwn(t *testing.T) {
	runs := &fakeMajorUpgrades{status: &service.MajorUpgradeStatus{CurrentVersion: "16", Targets: []string{"17"}, Supported: true, Blockers: []string{}}}
	w, body := callMajorUpgrade(majorUpgradeRouter(runs, nil), http.MethodGet, "")
	if w.Code != 200 || runs.statusOf != "p1" || body["currentVersion"] != "16" {
		t.Fatalf("%d %v", w.Code, body)
	}
	runs.err = fmt.Errorf("x: %w", service.ErrProjectNotFound)
	if w, _ := callMajorUpgrade(majorUpgradeRouter(runs, nil), http.MethodGet, ""); w.Code != 404 {
		t.Fatalf("missing project: %d", w.Code)
	}
	runs.err = errors.New("platform db: connection refused at 10.0.0.1")
	w, body = callMajorUpgrade(majorUpgradeRouter(runs, nil), http.MethodGet, "")
	if w.Code != 500 || strings.Contains(fmt.Sprint(body), "10.0.0.1") {
		t.Fatalf("an internal failure leaked: %d %v", w.Code, body)
	}
}

func TestMajorUpgradeStartAnswersTheRunAndAuditsIt(t *testing.T) {
	runs := &fakeMajorUpgrades{}
	audit := &recordingAudit{}
	w, body := callMajorUpgrade(majorUpgradeRouter(runs, audit), http.MethodPost, `{"targetVersion":"17","confirm":true}`)
	if w.Code != http.StatusAccepted || runs.req.TargetVersion != "17" || !runs.req.Confirm {
		t.Fatalf("%d %v", w.Code, body)
	}
	upgrade, _ := body["upgrade"].(map[string]any)
	if upgrade["id"] != "mu-1" || upgrade["requestedBy"] != nil {
		t.Fatalf("upgrade = %v", upgrade)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "database.major_upgrade.start" || audit.entries[0].ProjectID != "p1" {
		t.Fatalf("audit = %+v", audit.entries)
	}
}

func TestMajorUpgradeStartRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		err  error
		code int
	}{
		{"unknown field", `{"targetVersion":"17","confirm":true,"force":true}`, nil, 400},
		{"unconfirmed", `{"targetVersion":"17"}`, domain.ErrMajorUpgradeUnconfirmed, 400},
		{"bad target", `{"targetVersion":"x","confirm":true}`, domain.ErrMajorUpgradeTarget, 400},
		{"not offered", `{"targetVersion":"15","confirm":true}`, service.ErrMajorUpgradeTargetNotOffered, 400},
		{"unsupported", `{"targetVersion":"17","confirm":true}`, fmt.Errorf("%w: mysql", service.ErrMajorUpgradeUnsupported), 409},
		{"no backups", `{"targetVersion":"17","confirm":true}`, service.ErrMajorUpgradeNeedsBackups, 409},
		{"busy", `{"targetVersion":"17","confirm":true}`, service.ErrProjectOperationRunning, 409},
		{"running", `{"targetVersion":"17","confirm":true}`, storage.ErrMajorUpgradeRunning, 409},
		{"not active", `{"targetVersion":"17","confirm":true}`, fmt.Errorf("p1 is PAUSED; %w", service.ErrProjectNotActive), 409},
		{"check unavailable", `{"targetVersion":"17","confirm":true}`, service.ErrMajorUpgradeCheckUnavailable, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := &fakeMajorUpgrades{err: tc.err}
			audit := &recordingAudit{}
			w, body := callMajorUpgrade(majorUpgradeRouter(runs, audit), http.MethodPost, tc.body)
			if w.Code != tc.code {
				t.Fatalf("%d %v", w.Code, body)
			}
			if len(audit.entries) != 0 {
				t.Fatal("a refused start was audited")
			}
		})
	}
}

func TestMajorUpgradeStartListsTheBlockers(t *testing.T) {
	runs := &fakeMajorUpgrades{err: &service.MajorUpgradeBlockedError{Blockers: []string{"1 prepared transaction(s) are open"}}}
	w, body := callMajorUpgrade(majorUpgradeRouter(runs, nil), http.MethodPost, `{"targetVersion":"17","confirm":true}`)
	blockers, _ := body["blockers"].([]any)
	if w.Code != 409 || len(blockers) != 1 {
		t.Fatalf("%d %v", w.Code, body)
	}
}
