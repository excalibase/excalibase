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

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
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
	lifecycleErr    error
	lifecycleCalls  []string
	resumedBy       string
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

func (f *fakeAppDeployer) lifecycle(op, projectID, appID string, status string) (*apphost.App, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, op+":"+projectID+"/"+appID)
	if f.lifecycleErr != nil {
		return nil, f.lifecycleErr
	}
	return &apphost.App{ID: appID, ProjectID: projectID, Status: status}, nil
}

func (f *fakeAppDeployer) PauseApp(_ context.Context, projectID, appID string) (*apphost.App, error) {
	return f.lifecycle("pause", projectID, appID, apphost.StatusStopped)
}

func (f *fakeAppDeployer) ResumeApp(_ context.Context, projectID, appID, actor string) (*apphost.App, error) {
	f.resumedBy = actor
	return f.lifecycle("resume", projectID, appID, apphost.StatusRunning)
}

func (f *fakeAppDeployer) DeleteApp(_ context.Context, projectID, appID string, _ bool) error {
	_, err := f.lifecycle("delete", projectID, appID, "")
	return err
}

func (f *fakeAppDeployer) PauseAppInBackground(_ context.Context, projectID, appID string) (*apphost.App, error) {
	return f.lifecycle("pause-async", projectID, appID, apphost.StatusRunning)
}

func (f *fakeAppDeployer) ResumeAppInBackground(_ context.Context, projectID, appID, actor string) (*apphost.App, error) {
	f.resumedBy = actor
	return f.lifecycle("resume-async", projectID, appID, apphost.StatusStopped)
}

func (f *fakeAppDeployer) DeleteAppInBackground(_ context.Context, projectID, appID string, confirmDeleteDisk bool) (*apphost.App, error) {
	op := "delete-async"
	if confirmDeleteDisk {
		op += "-confirmed"
	}
	return f.lifecycle(op, projectID, appID, apphost.StatusRunning)
}

func (f *fakeAppDeployer) ResizeAppDisk(context.Context, string, string, string) (*apphost.App, error) {
	return nil, errors.New("not used")
}

func (f *fakeAppDeployer) AppDiskStatus(context.Context, string, string) (*service.AppDiskReport, error) {
	return nil, errors.New("not used")
}

func setupAppDeployRouter(t *testing.T, deployer *fakeAppDeployer) chi.Router {
	t.Helper()
	h := NewAppDeployHandler(deployer)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		r.Post("/deploy", h.Deploy)
		r.Get("/deploys", h.ListDeploys)
		r.Post("/deploys/{deployId}/redeploy", h.Redeploy)
		r.Post("/pause", h.Pause)
		r.Post("/resume", h.Resume)
		r.Delete("/", h.Delete)
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

func TestAppDeployHandler_Deploy_PlanRefusals(t *testing.T) {
	for err, code := range map[error]int{
		fmt.Errorf("%w: the FREE plan allows at most 1", service.ErrAppOverPlan): http.StatusConflict,
		fmt.Errorf("%w: organisation missing", service.ErrOrgTierUnresolved):     http.StatusInternalServerError,
	} {
		deployer := newFakeAppDeployer()
		deployer.deployErr = err
		rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
		if rec.Code != code {
			t.Errorf("%v: got %d want %d", err, rec.Code, code)
		}
		if strings.Contains(rec.Body.String(), "organisation missing") {
			t.Errorf("detail leaked: %s", rec.Body.String())
		}
	}
}

func TestAppDeployHandler_Resume_AdmissionRefusals(t *testing.T) {
	for err, code := range map[error]int{
		fmt.Errorf("%w: the FREE plan allows at most 1", service.ErrAppOverPlan): http.StatusConflict,
		fmt.Errorf("%w: organisation missing", service.ErrOrgTierUnresolved):     http.StatusInternalServerError,
		fmt.Errorf("%w: organisation missing", service.ErrAppCapacity):           http.StatusServiceUnavailable,
		fmt.Errorf("%w: organisation missing", service.ErrAppNoSandboxNode):      http.StatusServiceUnavailable,
	} {
		deployer := newFakeAppDeployer()
		deployer.lifecycleErr = err
		rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/resume")
		if rec.Code != code {
			t.Errorf("%v: got %d want %d", err, rec.Code, code)
		}
		if strings.Contains(rec.Body.String(), "organisation missing") {
			t.Errorf("detail leaked: %s", rec.Body.String())
		}
	}
}

func TestAppDeployHandler_Resume_NamesTheCallerAndSaysWhatThePlanAllows(t *testing.T) {
	deployer := newFakeAppDeployer()
	rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/resume")
	if rec.Code != http.StatusOK || deployer.resumedBy != "dev-1" {
		t.Fatalf("code %d resumed by %q", rec.Code, deployer.resumedBy)
	}

	deployer.lifecycleErr = fmt.Errorf("%w: the FREE plan allows at most 1 copies and the app was paused with 3; scale it down to 1 or redeploy it", service.ErrAppOverPlan)
	rec = doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/resume")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "allows at most 1") {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
}
