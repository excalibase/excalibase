package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeTemplates struct {
	views     []service.TemplateView
	err       error
	project   string
	template  string
	mayChange bool
	opts      service.TemplateDeployOptions
	result    *service.TemplateDeployResult
}

func (f *fakeTemplates) List(_ context.Context, projectID string, mayChange bool) ([]service.TemplateView, error) {
	f.project, f.mayChange = projectID, mayChange
	return f.views, f.err
}

func (f *fakeTemplates) Get(_ context.Context, projectID, templateID string, mayChange bool) (service.TemplateView, error) {
	f.project, f.template, f.mayChange = projectID, templateID, mayChange
	if f.err != nil {
		return service.TemplateView{}, f.err
	}
	return service.TemplateView{ID: templateID, Source: "format: excalibase.template/v1"}, nil
}

func (f *fakeTemplates) Deploy(_ context.Context, projectID, templateID, _ string, opts service.TemplateDeployOptions) (*service.TemplateDeployResult, error) {
	f.project, f.template, f.opts = projectID, templateID, opts
	return f.result, f.err
}

func templateRouter(api AppTemplateAPI, access *custommw.ProjectAccess) http.Handler {
	h := NewAppTemplateHandler(api)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(custommw.WithProjectAccess(req.Context(), access)))
		})
	})
	r.Route("/api/projects/{projectId}/app-templates", h.Routes)
	return r
}

var developerAccess = &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleDeveloper}}

func callTemplates(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/projects/proj-abc/app-templates"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestTemplateListSaysWhetherTheCallerMayOpenTheNetwork(t *testing.T) {
	api := &fakeTemplates{views: []service.TemplateView{{ID: "redis"}}}
	w := callTemplates(templateRouter(api, developerAccess), http.MethodGet, "/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"redis"`) || api.mayChange {
		t.Fatalf("developer: %d %s mayChange=%v", w.Code, w.Body, api.mayChange)
	}
	callTemplates(templateRouter(api, adminAccess), http.MethodGet, "/", "")
	if !api.mayChange || api.project != "proj-abc" {
		t.Fatalf("admin: mayChange=%v project=%s", api.mayChange, api.project)
	}
}

func TestTemplateGetAndUnknown(t *testing.T) {
	api := &fakeTemplates{}
	w := callTemplates(templateRouter(api, developerAccess), http.MethodGet, "/redis", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "excalibase.template/v1") {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	api.err = service.ErrTemplateNotFound
	if w := callTemplates(templateRouter(api, developerAccess), http.MethodGet, "/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := callTemplates(templateRouter(api, developerAccess), http.MethodGet, "/Bad_Id", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestTemplateDeployPassesTheRoleAndTheConfirmation(t *testing.T) {
	api := &fakeTemplates{result: &service.TemplateDeployResult{TemplateID: "web-redis"}}
	w := callTemplates(templateRouter(api, adminAccess), http.MethodPost, "/web-redis/deploy", `{"confirmPrivateNetwork":true}`)
	if w.Code != http.StatusAccepted || !api.opts.ConfirmPrivateNetwork || !api.opts.MayChangePrivateNetwork || api.template != "web-redis" {
		t.Fatalf("admin: %d %s %+v", w.Code, w.Body, api.opts)
	}
	w = callTemplates(templateRouter(api, developerAccess), http.MethodPost, "/web-redis/deploy", "")
	if w.Code != http.StatusAccepted || api.opts.ConfirmPrivateNetwork || api.opts.MayChangePrivateNetwork {
		t.Fatalf("developer, empty body: %d %s %+v", w.Code, w.Body, api.opts)
	}
}

func TestTemplateDeployRefusesABadBody(t *testing.T) {
	api := &fakeTemplates{result: &service.TemplateDeployResult{}}
	for _, body := range []string{`{"confirmPrivateNetwork":"yes"}`, `{"hostPath":"/"}`, `not json`} {
		if w := callTemplates(templateRouter(api, adminAccess), http.MethodPost, "/redis/deploy", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
}

func TestTemplateDeployErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
		body string
	}{
		{service.ErrTemplateNotFound, http.StatusNotFound, "template not found"},
		{&service.TemplateRefusedError{Reasons: []string{"the template needs 2 apps"}}, http.StatusConflict, "needs 2 apps"},
		{service.ErrTemplateNetworkNeedsAdmin, http.StatusForbidden, "admin or owner"},
		{service.ErrTemplateNetworkUnconfirmed, http.StatusConflict, "confirm_private_network"},
		{&service.TemplateFailedError{Step: `deploy app "web": no room`, Cause: errors.New(`deploy app "web": no room`)}, http.StatusConflict, "everything it created was removed"},
		{&service.TemplateFailedError{Step: `could not create app "web"`, Cause: errors.New(`pq: duplicate key 10.0.0.1`), ServerFault: true}, http.StatusInternalServerError, `could not create app \"web\"`},
		{&service.TemplateRollbackError{Cause: errors.New("x"), Remaining: []string{`app "redis"`}}, http.StatusInternalServerError, `app \"redis\"`},
		{service.ErrProjectOperationRunning, http.StatusConflict, "another operation"},
		{service.ErrOrgTierUnresolved, http.StatusInternalServerError, ""},
		{errors.New("pq: connection refused 10.0.0.1"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		api := &fakeTemplates{err: tc.err}
		w := callTemplates(templateRouter(api, adminAccess), http.MethodPost, "/redis/deploy", "{}")
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%v: %d %s", tc.err, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "10.0.0.1") {
			t.Errorf("an internal detail leaked: %s", w.Body)
		}
	}
}

func TestTemplateRefusalListsItsReasons(t *testing.T) {
	api := &fakeTemplates{err: &service.TemplateRefusedError{Reasons: []string{"one", "two"}}}
	w := callTemplates(templateRouter(api, adminAccess), http.MethodPost, "/redis/deploy", "{}")
	var body struct {
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Reasons) != 2 {
		t.Fatalf("%s (%v)", w.Body, err)
	}
}
