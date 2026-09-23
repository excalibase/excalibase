package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

const deployHandlerProject = "proj_deployh1"

type fakeAppDeployer struct {
	deployApp       map[string]*apphost.Deploy // keyed projectID+"/"+appID
	deployErr       error
	lastActor       string
	listDeploys     []*apphost.Deploy
	listErr         error
	lastListArgs    [2]string // projectID, appID of the last ListDeploys call
	lastLimit       int
	redeployApp     map[string]*apphost.Deploy // keyed projectID+"/"+appID+"/"+deployID
	redeployErr     error
	lastRedeployIDs [3]string // projectID, appID, deployID of the last RedeployApp call
}

func newFakeAppDeployer() *fakeAppDeployer {
	return &fakeAppDeployer{deployApp: map[string]*apphost.Deploy{}, redeployApp: map[string]*apphost.Deploy{}}
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

func (f *fakeAppDeployer) RedeployApp(_ context.Context, projectID, appID, deployID, actor string) (*apphost.Deploy, error) {
	f.lastActor = actor
	f.lastRedeployIDs = [3]string{projectID, appID, deployID}
	if f.redeployErr != nil {
		return nil, f.redeployErr
	}
	if deploy, ok := f.redeployApp[projectID+"/"+appID+"/"+deployID]; ok {
		return deploy, nil
	}
	return nil, apphost.ErrDeployNotFound
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
		r.Post("/deploys/{deployId}/redeploy", h.Redeploy)
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

func TestAppDeployHandler_ListDeploys_InternalError(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.listErr = errors.New("pq: connection refused")
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pq: ") {
		t.Errorf("the pq: prefix must be stripped: %s", rec.Body.String())
	}
}

func TestAppDeployHandler_Deploy_InternalError(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployErr = errors.New("pq: connection refused")
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Deploy_InvalidAppID(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/bad!id/deploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_ListDeploys_InvalidAppID(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/bad!id/deploys")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_ListDeploys_ValidLimit(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.listDeploys = []*apphost.Deploy{{ID: "dep-1"}}
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	if deployer.lastLimit != 5 {
		t.Errorf("limit: got %d want 5", deployer.lastLimit)
	}
}

func TestAppDeployHandler_Deploy_InvalidProjectID(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/bad!project/apps/app-1/deploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Deploy_ActorFallsBackToUnknown(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"] = &apphost.Deploy{ID: "dep-1", ProjectID: deployHandlerProject, AppID: "app-1"}
	r := setupAppDeployRouter(t, deployer)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rec.Code, rec.Body.String())
	}
	if deployer.lastActor != "unknown" {
		t.Errorf("actor: got %q want unknown", deployer.lastActor)
	}
}

func sampleMaskedDeploy(id string) *apphost.Deploy {
	return &apphost.Deploy{
		ID: id, AppID: "app-1", ProjectID: deployHandlerProject, Revision: 3,
		Image: "ghcr.io/acme/storefront:1.4.2",
		Spec: apphost.DeploySpec{
			Image: "ghcr.io/acme/storefront:1.4.2",
			Env: []apphost.EnvSummary{
				{Name: "API_KEY", Kind: apphost.KindLiteral},
				{Name: "DATABASE_URL", Kind: apphost.KindReference},
			},
			Port: 8080, Replicas: 1,
		},
		Config: apphost.DeployConfig{
			Image: "ghcr.io/acme/storefront:1.4.2",
			Env: []apphost.EnvVar{
				{Name: "API_KEY", Kind: apphost.KindLiteral, Value: strPtr("sk_live_should_never_leak")},
				{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
					SourceKind: apphost.SourceDatabase, SourceName: "db1", Variable: "DATABASE_URL",
				}},
			},
			Port: 8080, Replicas: 1,
		},
		RedeployOf: "dep-1",
		Status:     apphost.DeployStatusRolling,
	}
}

func strPtr(s string) *string { return &s }

func TestAppDeployHandler_Redeploy_Accepted(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.redeployApp[deployHandlerProject+"/app-1/dep-1"] = sampleMaskedDeploy("dep-2")
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-1/redeploy")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var got apphost.Deploy
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "dep-2" || got.RedeployOf != "dep-1" {
		t.Errorf("body: got %+v", got)
	}
	if deployer.lastRedeployIDs != [3]string{deployHandlerProject, "app-1", "dep-1"} {
		t.Errorf("scoped args: got %+v", deployer.lastRedeployIDs)
	}
	if deployer.lastActor != "dev-1" {
		t.Errorf("actor: got %q want dev-1", deployer.lastActor)
	}
}

func TestAppDeployHandler_Redeploy_DeployNotFound(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/missing/redeploy")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Redeploy_AppNotFound(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.redeployErr = apphost.ErrAppNotFound
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/missing/deploys/dep-1/redeploy")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Redeploy_InvalidDeployID(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/bad!id/redeploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Redeploy_InvalidAppID(t *testing.T) {
	deployer := newFakeAppDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/bad!id/deploys/dep-1/redeploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppDeployHandler_Redeploy_InternalError(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.redeployErr = errors.New("pq: connection refused")
	r := setupAppDeployRouter(t, deployer)

	rec := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-1/redeploy")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pq: ") {
		t.Errorf("the pq: prefix must be stripped: %s", rec.Body.String())
	}
}

// EXC-386b: the deploy JSON the API returns must never carry a literal env
// value or a secret reference — only names and kinds. This is checked across
// Deploy, Redeploy and ListDeploys since all three marshal the same type.
func TestAppDeployHandler_ResponsesNeverLeakEnvValues(t *testing.T) {
	secretLiteral := "sk_live_should_never_leak"
	deploy := sampleMaskedDeploy("dep-2")
	deployer := newFakeAppDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"] = deploy
	deployer.redeployApp[deployHandlerProject+"/app-1/dep-1"] = deploy
	deployer.listDeploys = []*apphost.Deploy{deploy}
	r := setupAppDeployRouter(t, deployer)

	for _, req := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/projects/" + deployHandlerProject + "/apps/app-1/deploy"},
		{http.MethodPost, "/api/projects/" + deployHandlerProject + "/apps/app-1/deploys/dep-1/redeploy"},
		{http.MethodGet, "/api/projects/" + deployHandlerProject + "/apps/app-1/deploys"},
	} {
		rec := doDeployRequest(t, r, req.method, req.path)
		body := rec.Body.String()
		if strings.Contains(body, secretLiteral) {
			t.Fatalf("%s %s: response leaked a literal value: %s", req.method, req.path, body)
		}
		if strings.Contains(body, "\"config\"") {
			t.Fatalf("%s %s: response must never carry the frozen config: %s", req.method, req.path, body)
		}
	}
}
