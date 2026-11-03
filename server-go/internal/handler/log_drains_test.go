package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

var drainKey = testutil.FixtureSecret("drain-key")

type noLogs struct{}

func (noLogs) ReadLines(_ context.Context, _ string, from, _ int64, _ int) ([]logdrain.Line, error) {
	return []logdrain.Line{{TS: from, Line: "<b>LOG:  checkpoint</b>"}}, nil
}
func (noLogs) CountLines(context.Context, string, int64, int64) (int64, error) { return 0, nil }

func newDrainService(t *testing.T) (*service.LogDrainService, *fakestore.LogDrains) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Init(1, 1); err != nil {
		t.Fatal(err)
	}
	store := fakestore.NewLogDrains()
	instances := map[string]*domain.DatabaseInstance{
		"proj-a": {ProjectID: "proj-a", Namespace: "org-proj-a", DeploymentMode: domain.ModeK8s},
		"proj-b": {ProjectID: "proj-b", Namespace: "org-proj-b", DeploymentMode: domain.ModeK8s},
	}
	svc := service.NewLogDrainService(service.LogDrainDeps{Store: store, Vault: v, Logs: noLogs{},
		Instances: func(projectID string) (*domain.DatabaseInstance, error) {
			if inst, ok := instances[projectID]; ok {
				return inst, nil
			}
			return nil, service.ErrProjectNotFound
		}})
	return svc, store
}

func drainRouter(t *testing.T) (http.Handler, *recordedAudit) {
	t.Helper()
	svc, _ := newDrainService(t)
	audit := &recordedAudit{}
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/log-drains", NewLogDrainsHandler(svc, audit).Routes)
	return r, audit
}

func drainCall(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "admin-1", Active: true}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const drainsA = "/api/projects/proj-a/log-drains"

func createDatadog(t *testing.T, router http.Handler) logDrainView {
	t.Helper()
	rec := drainCall(router, http.MethodPost, drainsA, `{"name":"dd","kind":"datadog","datadogSite":"us1","secret":"`+drainKey+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), drainKey) {
		t.Fatal("the key came back in the create answer")
	}
	var view logDrainView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestLogDrainsHandler_KeyIsWriteOnly(t *testing.T) {
	router, audit := drainRouter(t)
	view := createDatadog(t, router)
	if !view.HasSecret || view.Kind != "datadog" || view.DatadogSite != "us1" || view.Status != domain.LogDrainActive {
		t.Errorf("view %+v", view)
	}
	rec := drainCall(router, http.MethodGet, drainsA, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), drainKey) || !strings.Contains(rec.Body.String(), `"hasSecret":true`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("list cache %q", rec.Header().Get("Cache-Control"))
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "log_drain.create" || strings.Contains(audit.entries[0].Details, drainKey) {
		t.Errorf("audit %+v", audit.entries)
	}
}

func TestLogDrainsHandler_Refusals(t *testing.T) {
	router, _ := drainRouter(t)
	cases := []struct {
		name, body string
		code       int
	}{
		{"not json", `{`, http.StatusBadRequest},
		{"unknown field", `{"name":"x","kind":"https","url":"https://logs.example.com/","cursor":1}`, http.StatusBadRequest},
		{"http url", `{"name":"x","kind":"https","url":"http://logs.example.com/"}`, http.StatusBadRequest},
		{"metadata url", `{"name":"x","kind":"https","url":"https://169.254.169.254/"}`, http.StatusBadRequest},
		{"too big", `{"name":"` + strings.Repeat("x", 20<<10) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := drainCall(router, http.MethodPost, drainsA, tc.body); rec.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	for range service.MaxLogDrainsPerProject {
		createDatadog(t, router)
	}
	rec := drainCall(router, http.MethodPost, drainsA, `{"name":"4","kind":"datadog","datadogSite":"us1","secret":"k"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "at most 3") {
		t.Errorf("limit: %d %s", rec.Code, rec.Body)
	}
	rec = drainCall(router, http.MethodPost, "/api/projects/proj-gone/log-drains", `{"name":"x","kind":"datadog","datadogSite":"us1","secret":"k"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", rec.Code)
	}
}

func TestLogDrainsHandler_UpdateAndDeleteStayInTheProject(t *testing.T) {
	router, audit := drainRouter(t)
	view := createDatadog(t, router)
	foreign := "/api/projects/proj-b/log-drains/" + view.ID
	if rec := drainCall(router, http.MethodPatch, foreign, `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("foreign patch: %d", rec.Code)
	}
	if rec := drainCall(router, http.MethodDelete, foreign, ""); rec.Code != http.StatusNotFound {
		t.Errorf("foreign delete: %d", rec.Code)
	}
	own := drainsA + "/" + view.ID
	rec := drainCall(router, http.MethodPatch, own, `{"name":"renamed","enabled":false,"secret":"replaced-key"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "replaced-key") {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	var updated logDrainView
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "renamed" || updated.Enabled || updated.Status != domain.LogDrainDisabled {
		t.Errorf("updated %+v", updated)
	}
	if rec := drainCall(router, http.MethodPatch, own, `{"kind":"https"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("kind is fixed: %d", rec.Code)
	}
	if rec := drainCall(router, http.MethodDelete, own, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := drainCall(router, http.MethodDelete, own, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}
	for _, entry := range audit.entries {
		if strings.Contains(entry.Details, "replaced-key") || strings.Contains(entry.Details, drainKey) {
			t.Errorf("audit leaks a key: %+v", entry)
		}
	}
	if len(audit.entries) != 3 {
		t.Errorf("audit entries %d", len(audit.entries))
	}
}

func TestLogDrainsHandler_UnavailableIsConflict(t *testing.T) {
	v, _ := vault.NewWithStore(vault.NewMemoryStore())
	_, _ = v.Init(1, 1)
	svc := service.NewLogDrainService(service.LogDrainDeps{Store: fakestore.NewLogDrains(), Vault: v,
		Instances: func(string) (*domain.DatabaseInstance, error) {
			return &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "ns", DeploymentMode: domain.ModeK8s}, nil
		}})
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/log-drains", NewLogDrainsHandler(svc, nil).Routes)
	rec := drainCall(r, http.MethodPost, drainsA, `{"name":"x","kind":"datadog","datadogSite":"us1","secret":"k"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("no log store: %d %s", rec.Code, rec.Body)
	}
}

func forwarderRouter(t *testing.T, token string) (http.Handler, *fakestore.LogDrains, *service.LogDrainService) {
	t.Helper()
	svc, store := newDrainService(t)
	r := chi.NewRouter()
	NewLogForwarderHandler(svc, token).Mount(r)
	return r, store, svc
}

func forwarderCall(router http.Handler, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set(logdrain.TokenHeader, token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestLogForwarderHandler_RequiresTheForwarderToken(t *testing.T) {
	router, _, _ := forwarderRouter(t, "forwarder-token-0123456789")
	for _, token := range []string{"", "wrong", "forwarder-token-012345678", "forwarder-token-0123456789x"} {
		for _, path := range []string{logdrain.PullPath, logdrain.AckPath} {
			if rec := forwarderCall(router, path, token, `{}`); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %q: %d", path, token, rec.Code)
			}
		}
	}
	// No configured token never opens the routes.
	open, _, _ := forwarderRouter(t, "")
	if rec := forwarderCall(open, logdrain.PullPath, "", `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("unconfigured token: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, logdrain.PullPath, nil)
	req.Header.Set("Authorization", "Bearer forwarder-token-0123456789")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the token in Authorization is not the forwarder header: %d", rec.Code)
	}
}

func TestLogForwarderHandler_PullAndAck(t *testing.T) {
	const token = "forwarder-token-0123456789"
	router, store, svc := forwarderRouter(t, token)
	drain, err := svc.Create(context.Background(), "proj-a", "u", service.CreateLogDrainInput{
		Name: "h", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/in"})
	if err != nil {
		t.Fatal(err)
	}
	// Start the drain a minute back so there is a window to read.
	started := store.Items[drain.ID]
	started.CursorNS -= int64(time.Minute)
	started.NextAttemptAt = started.NextAttemptAt.Add(-time.Minute)
	store.Items[drain.ID] = started

	rec := forwarderCall(router, logdrain.PullPath, token, `{"max":2}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("pull: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "<b>LOG:") {
		t.Errorf("the answer escapes HTML: %s", rec.Body)
	}
	var pulled logdrain.PullResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pulled); err != nil || len(pulled.Batches) != 1 {
		t.Fatalf("pull body %s: %v", rec.Body, err)
	}
	batch := pulled.Batches[0]
	if batch.Target != "https://logs.example.com/in" || batch.ProjectID != "proj-a" {
		t.Errorf("batch %+v", batch)
	}
	ack := logdrain.Ack{DrainID: drain.ID, From: batch.From, To: batch.To, Lines: len(batch.Lines)}
	body, _ := json.Marshal(ack)
	if rec := forwarderCall(router, logdrain.AckPath, token, string(body)); rec.Code != http.StatusNoContent {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
	if stored := store.Items[drain.ID]; stored.SentLines != 1 || stored.CursorNS != ack.To {
		t.Errorf("after ack %+v", stored)
	}
	if rec := forwarderCall(router, logdrain.AckPath, token, `{"drainId":"x","from":"5","to":"1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad ack: %d", rec.Code)
	}
	if rec := forwarderCall(router, logdrain.AckPath, token, `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage ack: %d", rec.Code)
	}
	if rec := forwarderCall(router, logdrain.PullPath, token, `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage pull: %d", rec.Code)
	}
}

func TestLogDrainsHandler_ShowsOnlyTheURLsHost(t *testing.T) {
	router, _ := drainRouter(t)
	rec := drainCall(router, http.MethodPost, drainsA, `{"name":"h","kind":"https","url":"https://logs.example.com/receiver/v1/TOKEN"}`)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "TOKEN") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if list := drainCall(router, http.MethodGet, drainsA, ""); strings.Contains(list.Body.String(), "TOKEN") || !strings.Contains(list.Body.String(), "https://logs.example.com/...") {
		t.Errorf("list %s", list.Body)
	}
}
