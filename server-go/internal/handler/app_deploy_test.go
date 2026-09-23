package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

const deployHandlerProject = "proj_deployh1"

type fakeAppDeployer struct {
	deployApp    map[string]*apphost.Deploy // keyed projectID+"/"+appID
	deployErr    error
	lastActor    string
	listDeploys  []*apphost.Deploy
	listErr      error
	lastListArgs [2]string // projectID, appID of the last ListDeploys call
	lastLimit    int
}

func newFakeAppDeployer() *fakeAppDeployer {
	return &fakeAppDeployer{deployApp: map[string]*apphost.Deploy{}}
}

func (f *fakeAppDeployer) DeployApp(_ context.Context, projectID, appID, actor string) (*apphost.Deploy, error) {
	f.lastActor = actor
	if f.deployErr != nil {
		return nil, f.deployErr
	}
	if deploy, ok := f.deployApp[projectID+"/"+appID]; ok {
		return deploy, nil
	}
	return nil, apphost.ErrAppNotFound
}

func (f *fakeAppDeployer) ListDeploys(projectID, appID string, limit int) ([]*apphost.Deploy, error) {
	f.lastListArgs = [2]string{projectID, appID}
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listDeploys, nil
}

func setupAppDeployRouter(t *testing.T, deployer *fakeAppDeployer) chi.Router {
	t.Helper()
	h := NewAppDeployHandler(deployer)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		r.Post("/deploy", h.Deploy)
		r.Get("/deploys", h.ListDeploys)
	})
	return r
}

func doDeployRequest(t *testing.T, r chi.Router, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "dev-1", Active: true}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAppDeployHandler_Deploy_Accepted(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"] = &apphost.Deploy{
		ID: "dep-1", AppID: "app-1", ProjectID: deployHandlerProject, Revision: 1,
		Status: apphost.DeployStatusRolling,
	}
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var got apphost.Deploy
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "dep-1" || got.Status != apphost.DeployStatusRolling {
		t.Errorf("body: got %+v", got)
	}
	if deployer.lastActor != "dev-1" {
		t.Errorf("actor: got %q want dev-1", deployer.lastActor)
	}
}

func TestAppDeployHandler_Deploy_AppNotFound(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/missing/deploy")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Deploy_CrossProjectIsNotFound(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployApp["other-project/app-1"] = &apphost.Deploy{ID: "dep-1", AppID: "app-1", ProjectID: "other-project"}
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Deploy_AlwaysAccepted(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"] = &apphost.Deploy{
		ID: "dep-2", AppID: "app-1", ProjectID: deployHandlerProject, Revision: 2,
		Status: apphost.DeployStatusRolling,
	}
	r := setupAppDeployRouter(t, deployer)

	for i := 0; i < 2; i++ {
		rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("call %d: status: got %d want 202, body=%s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestAppDeployHandler_ListDeploys(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.listDeploys = []*apphost.Deploy{
		{ID: "dep-2", Revision: 2}, {ID: "dep-1", Revision: 1},
	}
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got []apphost.Deploy
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Revision != 2 {
		t.Errorf("body: got %+v", got)
	}
	if deployer.lastListArgs != [2]string{deployHandlerProject, "app-1"} {
		t.Errorf("scoped args: got %+v", deployer.lastListArgs)
	}
}

func TestAppDeployHandler_ListDeploys_InvalidLimit(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys?limit=0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}
