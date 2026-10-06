package handler

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

// EXC-554: with the pipeline dark, Studio's deploy runs as it did before
// EXC-542/543/545, and what those added answers as if it did not exist.

const pipelineDeployPath = "/api/projects/" + deployHandlerProject + "/apps/app-1/deploy"

func darkPipelineDeployRouter(t *testing.T, deployer *fakeAppDeployer) chi.Router {
	t.Helper()
	return setupAppDeployRouterWith(t, deployer, features.NewStatic())
}

func TestPipelineOff_ADeployNamingAnImageIsNotFound(t *testing.T) {
	deployer := ciDeployer()
	r := darkPipelineDeployRouter(t, deployer)
	// A malformed body must not answer the pipeline's 400, which names its fields.
	for _, body := range []string{`{"image":"ghcr.io/acme/web:main"}`, `{"commitSha":"` + ciCommit + `"}`,
		`{"commitSha":"zzz"}`, `{"foo":1}`, `not json`} {
		rec := ciDeployRequest(t, r, http.MethodPost, pipelineDeployPath, body, ciToken)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", body, rec.Code)
		}
	}
	if deployer.lastImage != "" || deployer.lastActor != "" {
		t.Fatal("nothing is deployed when the pipeline is off")
	}
}

func TestPipelineOff_StudioDeployRunsTheAppAsItIs(t *testing.T) {
	for _, body := range []string{"", "{}"} {
		deployer := ciDeployer()
		rec := ciDeployRequest(t, darkPipelineDeployRouter(t, deployer), http.MethodPost, pipelineDeployPath, body, studioCookie)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("body %q: status %d: %s", body, rec.Code, rec.Body.String())
		}
		if deployer.deployedCurrent {
			t.Errorf("body %q: the digest-pinning deploy is part of the pipeline", body)
		}
		if deployer.lastActor != "dev-1" {
			t.Errorf("body %q: deploy not run as the caller: %q", body, deployer.lastActor)
		}
	}
}

func TestPipelineOn_StudioDeployPinsTheCurrentImage(t *testing.T) {
	deployer := ciDeployer()
	r := setupAppDeployRouterWith(t, deployer, features.NewStatic(features.Pipeline))
	rec := ciDeployRequest(t, r, http.MethodPost, pipelineDeployPath, "", studioCookie)
	if rec.Code != http.StatusAccepted || !deployer.deployedCurrent {
		t.Fatalf("status %d, deployedCurrent=%v", rec.Code, deployer.deployedCurrent)
	}
}

func TestPipelineOff_StudioRedeployStillWorks(t *testing.T) {
	deployer := ciDeployer()
	deployer.redeployApp[deployHandlerProject+"/app-1/dep-0"] = deployer.deployApp[deployHandlerProject+"/app-1"]
	r := darkPipelineDeployRouter(t, deployer)
	rec := ciDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploys/dep-0/redeploy", "", studioCookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPipelineOff_AutoDeployCannotBeSet(t *testing.T) {
	r, _ := setupAppRouterWith(t, features.NewStatic())
	app := createAutoDeployApp(t, r, "ghcr.io/acme/storefront:main")
	path := "/api/projects/" + autoDeployProject + "/apps/" + app.ID
	for _, value := range []bool{true, false} {
		rec := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"autoDeploy": value}, app.Version)
		if rec.Code != http.StatusNotFound {
			t.Errorf("autoDeploy=%v: status %d, want 404", value, rec.Code)
		}
	}
}

func TestPipelineOff_AnEditWithoutAutoDeployStillWorks(t *testing.T) {
	r, _ := setupAppRouterWith(t, features.NewStatic())
	app := createAutoDeployApp(t, r, "ghcr.io/acme/storefront:main")
	path := "/api/projects/" + autoDeployProject + "/apps/" + app.ID
	rec := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"image": "ghcr.io/acme/storefront:next"}, app.Version)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
}
