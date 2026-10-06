package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
	"github.com/go-chi/chi/v5"
)

const ciCommit = "9fceb02d0ae598e95dc970b74767f19372d61af8"

func ciDeployRequest(t *testing.T, r chi.Router, method, path, body string, token *domain.AccessToken) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := auth.SetUser(req.Context(), &domain.User{ID: "dev-1", Active: true})
	if token != nil {
		ctx = auth.SetToken(ctx, token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

var (
	ciToken      = &domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: deployHandlerProject}
	studioCookie = &domain.AccessToken{Scopes: auth.ScopeSession}
)

func ciDeployer() *fakeAppDeployer {
	deployer := newFakeAppDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"] = &apphost.Deploy{
		ID: "dep-1", AppID: "app-1", ProjectID: deployHandlerProject, Revision: 3,
		Status: apphost.DeployStatusRolling, Image: "ghcr.io/acme/web@sha256:" + strings.Repeat("ab", 32),
		Digest: "sha256:" + strings.Repeat("ab", 32), ImageRef: "ghcr.io/acme/web:main",
		Spec: apphost.DeploySpec{URL: "https://web-abc.apps.example.com"},
	}
	return deployer
}

func TestDeploy_AnImageFromCIIsPinnedAndAnswered(t *testing.T) {
	deployer := ciDeployer()
	r := setupAppDeployRouter(t, deployer)

	body := `{"image":"ghcr.io/acme/web:main","commitSha":"` + ciCommit + `"}`
	rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy", body, ciToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if deployer.lastImage != "ghcr.io/acme/web:main" {
		t.Fatalf("image passed on = %q", deployer.lastImage)
	}
	want := apphost.DeployOrigin{Actor: "dev-1", Source: apphost.DeploySourceAPI, CommitSHA: ciCommit}
	if deployer.lastOrigin != want {
		t.Fatalf("origin = %+v, want %+v", deployer.lastOrigin, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{"id": "dep-1", "status": "rolling", "url": "https://web-abc.apps.example.com",
		"digest": "sha256:" + strings.Repeat("ab", 32), "imageRef": "ghcr.io/acme/web:main"} {
		if got[field] != value {
			t.Errorf("%s = %v, want %v", field, got[field], value)
		}
	}
	if _, leaked := got["config"]; leaked {
		t.Fatal("the frozen config must never reach the API")
	}
}

func TestDeploy_WithoutABodyRunsTheAppAsItIs(t *testing.T) {
	deployer := ciDeployer()
	r := setupAppDeployRouter(t, deployer)

	rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy", "", studioCookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if deployer.lastImage != "" || deployer.lastOrigin.Source != apphost.DeploySourceStudio || !deployer.deployedCurrent {
		t.Fatalf("image %q origin %+v current %v", deployer.lastImage, deployer.lastOrigin, deployer.deployedCurrent)
	}
}

// A registry the platform may not dial names both ways out.
func TestDeploy_APrivateRegistryRefusalSaysWhatToDoInstead(t *testing.T) {
	deployer := ciDeployer()
	deployer.deployErr = fmt.Errorf("%w: registry.local:5000", imagedigest.ErrNotPublic)
	r := setupAppDeployRouter(t, deployer)
	rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy",
		`{"image":"registry.local:5000/web:1"}`, ciToken)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "registry.local:5000") || !strings.Contains(body, "deploy with no image") || !strings.Contains(body, "public registry") {
		t.Fatalf("body = %s", body)
	}
}

func TestDeploy_RefusesABodyItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"a malformed body":   `{"image":`,
		"a misspelled field": `{"imgae":"ghcr.io/acme/web:main"}`,
		"a branch name":      `{"image":"ghcr.io/acme/web:main","commitSha":"main"}`,
		"an oversized body":  `{"image":"` + strings.Repeat("a", 5000) + `"}`,
	} {
		deployer := ciDeployer()
		r := setupAppDeployRouter(t, deployer)
		rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy", body, ciToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
		if deployer.lastOrigin.Actor != "" {
			t.Errorf("%s: nothing may be deployed", name)
		}
	}
}

func TestDeploy_RegistryAnswersMapToStatuses(t *testing.T) {
	for err, want := range map[error]int{
		apphost.ErrInvalidImage:                            http.StatusBadRequest,
		imagedigest.ErrNotFound:                            http.StatusUnprocessableEntity,
		imagedigest.ErrDenied:                              http.StatusUnprocessableEntity,
		imagedigest.ErrNotPublic:                           http.StatusUnprocessableEntity,
		imagedigest.ErrRateLimited:                         http.StatusServiceUnavailable,
		imagedigest.ErrUnavailable:                         http.StatusBadGateway,
		apphost.ErrAppVersionConflict:                      http.StatusConflict,
		fmt.Errorf("wrapped: %w", imagedigest.ErrNotFound): http.StatusUnprocessableEntity,
	} {
		deployer := ciDeployer()
		deployer.deployErr = err
		r := setupAppDeployRouter(t, deployer)
		rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy",
			`{"image":"ghcr.io/acme/web:main"}`, ciToken)
		if rec.Code != want {
			t.Errorf("%v: status %d, want %d", err, rec.Code, want)
		}
		if want == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
			t.Errorf("%v: a rate limit tells the caller when to retry", err)
		}
	}
}

func TestGetDeploy_IsWhatCIPolls(t *testing.T) {
	deployer := ciDeployer()
	deployer.getDeploy[deployHandlerProject+"/app-1/dep-1"] = &apphost.Deploy{
		ID: "dep-1", AppID: "app-1", Status: apphost.DeployStatusSucceeded,
		Spec: apphost.DeploySpec{URL: "https://web-abc.apps.example.com"},
	}
	r := setupAppDeployRouter(t, deployer)

	rec := ciDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-1", "", ciToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["status"] != "succeeded" || got["url"] != "https://web-abc.apps.example.com" {
		t.Fatalf("body = %v", got)
	}

	rec = ciDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-2", "", ciToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown deploy: %d", rec.Code)
	}
	rec = ciDeployRequest(t, r, http.MethodGet, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/bad%20id", "", ciToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid deploy id: %d", rec.Code)
	}
}

func TestRedeploy_RecordsTheCallersSource(t *testing.T) {
	deployer := ciDeployer()
	deployer.redeployApp[deployHandlerProject+"/app-1/dep-1"] = &apphost.Deploy{ID: "dep-9", Status: apphost.DeployStatusRolling}
	r := setupAppDeployRouter(t, deployer)

	rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-1/redeploy", "", ciToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if deployer.lastOrigin.Source != apphost.DeploySourceAPI {
		t.Fatalf("origin = %+v", deployer.lastOrigin)
	}
}
