package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

const waitDeployPath = "/api/projects/" + deployHandlerProject + "/apps/app-1/deploy"

// waitRouter serves POST .../deploy with the pipeline on, a short poll and the given longest wait.
func waitRouter(t *testing.T, deployer *fakeAppDeployer, longest time.Duration, flags ...features.Feature) chi.Router {
	t.Helper()
	if flags == nil {
		flags = features.All()
	}
	h := NewAppDeployHandler(deployer)
	h.SetFeatures(features.NewStatic(flags...))
	h.SetDeployWait(longest)
	h.waitPoll = time.Millisecond
	r := chi.NewRouter()
	r.Post("/api/projects/{projectId}/apps/{appId}/deploy", h.Deploy)
	return r
}

// statusesThen answers GetDeploy for dep-1 with each status in turn, repeating the last.
func statusesThen(deployer *fakeAppDeployer, statuses ...string) *atomic.Int32 {
	var calls atomic.Int32
	deployer.getDeployFunc = func(projectID, appID, deployID string) (*apphost.Deploy, error) {
		if projectID != deployHandlerProject || appID != "app-1" || deployID != "dep-1" {
			return nil, apphost.ErrDeployNotFound
		}
		index := min(int(calls.Add(1))-1, len(statuses)-1)
		deploy := &apphost.Deploy{ID: "dep-1", AppID: "app-1", ProjectID: deployHandlerProject, Status: statuses[index],
			Spec: apphost.DeploySpec{URL: "https://web-abc.apps.example.com"}}
		if statuses[index] == apphost.DeployStatusFailed {
			deploy.FailureReason = "the container exited: exec format error"
		}
		return deploy, nil
	}
	return &calls
}

func deployBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	return body
}

const ciBody = `{"image":"ghcr.io/acme/web:main","commitSha":"` + ciCommit + `"}`

func TestDeployWait_AnswersOnceTheDeployIsLive(t *testing.T) {
	deployer := ciDeployer()
	statusesThen(deployer, apphost.DeployStatusPending, apphost.DeployStatusRolling, apphost.DeployStatusSucceeded)
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := deployBody(t, rec)
	if body["id"] != "dep-1" || body["status"] != "succeeded" || body["url"] != "https://web-abc.apps.example.com" {
		t.Fatalf("body = %v", body)
	}
	if _, isError := body["error"]; isError {
		t.Fatalf("a live deploy is not an error: %v", body)
	}
}

// curl --fail turns each of these into a failed CI step, with the deploy and the reason in the body.
func TestDeployWait_AFinishedDeployThatIsNotLiveIsAnError(t *testing.T) {
	cases := map[string]struct {
		code int
		want string
	}{
		apphost.DeployStatusFailed:     {http.StatusUnprocessableEntity, "exec format error"},
		apphost.DeployStatusSuperseded: {http.StatusConflict, "newer deploy"},
	}
	for status, c := range cases {
		deployer := ciDeployer()
		statusesThen(deployer, apphost.DeployStatusRolling, status)
		rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d: %s", status, rec.Code, c.code, rec.Body.String())
			continue
		}
		body := deployBody(t, rec)
		if body["id"] != "dep-1" || body["status"] != status {
			t.Errorf("%s: the body must be the deploy: %v", status, body)
		}
		if message, _ := body["error"].(string); !strings.Contains(message, c.want) {
			t.Errorf("%s: error %q must say %q", status, message, c.want)
		}
	}
}

func TestDeployWait_StopsAtItsBoundAndSaysWhereToKeepPolling(t *testing.T) {
	deployer := ciDeployer()
	statusesThen(deployer, apphost.DeployStatusRolling)
	started := time.Now()
	rec := ciDeployRequest(t, waitRouter(t, deployer, 30*time.Millisecond), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the wait ran %s past a 30ms bound", elapsed)
	}
	body := deployBody(t, rec)
	message, _ := body["error"].(string)
	if body["status"] != "rolling" || !strings.Contains(message, "/deploys/dep-1") {
		t.Fatalf("body = %v", body)
	}
}

func TestDeployWait_TheCallerMayAskForAShorterWaitButNotALongerOne(t *testing.T) {
	deployer := ciDeployer()
	statusesThen(deployer, apphost.DeployStatusRolling)
	started := time.Now()
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Hour), http.MethodPost, waitDeployPath+"?wait=true&timeout=1", ciBody, ciToken)
	if rec.Code != http.StatusGatewayTimeout || time.Since(started) > 10*time.Second {
		t.Fatalf("a one second wait: status %d after %s", rec.Code, time.Since(started))
	}

	deployer = ciDeployer()
	statusesThen(deployer, apphost.DeployStatusRolling)
	started = time.Now()
	for _, timeout := range []string{"3600", "99999999999999"} {
		started = time.Now()
		rec = ciDeployRequest(t, waitRouter(t, deployer, 30*time.Millisecond), http.MethodPost, waitDeployPath+"?wait=true&timeout="+timeout, ciBody, ciToken)
		if rec.Code != http.StatusGatewayTimeout || time.Since(started) > 5*time.Second {
			t.Fatalf("timeout=%s must be held to the bound: status %d after %s", timeout, rec.Code, time.Since(started))
		}
	}
}

func TestDeployWait_RefusesWaitParametersItDoesNotKnowBeforeDeploying(t *testing.T) {
	for _, query := range []string{"?wait=yes", "?wait=true&timeout=0", "?wait=true&timeout=-5", "?wait=true&timeout=soon", "?timeout=30"} {
		deployer := ciDeployer()
		rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+query, ciBody, ciToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, rec.Code)
		}
		if deployer.lastOrigin.Actor != "" {
			t.Errorf("%s: nothing may be deployed", query)
		}
	}
}

func TestDeployWait_WithoutWaitTheAnswerIsUnchanged(t *testing.T) {
	for _, query := range []string{"", "?wait=false"} {
		deployer := ciDeployer()
		calls := statusesThen(deployer, apphost.DeployStatusSucceeded)
		rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+query, ciBody, ciToken)
		if rec.Code != http.StatusAccepted || deployBody(t, rec)["status"] != "rolling" || calls.Load() != 0 {
			t.Errorf("%q: status %d, polls %d: %s", query, rec.Code, calls.Load(), rec.Body.String())
		}
	}
}

// With the pipeline dark the deploy route answers as it did before it, wait included (EXC-554).
func TestDeployWait_IsDarkWithThePipeline(t *testing.T) {
	deployer := ciDeployer()
	calls := statusesThen(deployer, apphost.DeployStatusSucceeded)
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute, features.MCP), http.MethodPost, waitDeployPath+"?wait=true", "", studioCookie)
	if rec.Code != http.StatusAccepted || calls.Load() != 0 {
		t.Fatalf("status %d, polls %d", rec.Code, calls.Load())
	}
}

func TestDeployWait_ADeployAlreadyFinishedAnswersAtOnce(t *testing.T) {
	deployer := ciDeployer()
	deployer.deployApp[deployHandlerProject+"/app-1"].Status = apphost.DeployStatusSucceeded
	calls := statusesThen(deployer, apphost.DeployStatusRolling)
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
	if rec.Code != http.StatusOK || calls.Load() != 0 {
		t.Fatalf("status %d, polls %d", rec.Code, calls.Load())
	}
}

func TestDeployWait_ABriefReadFailureDoesNotFailTheWait(t *testing.T) {
	deployer := ciDeployer()
	var calls atomic.Int32
	deployer.getDeployFunc = func(_, _, _ string) (*apphost.Deploy, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("connection reset")
		}
		return &apphost.Deploy{ID: "dep-1", Status: apphost.DeployStatusSucceeded}, nil
	}
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeployWait_ADeployThatDisappearsIsNotFound(t *testing.T) {
	deployer := ciDeployer()
	deployer.getDeployFunc = func(_, _, _ string) (*apphost.Deploy, error) { return nil, apphost.ErrDeployNotFound }
	rec := ciDeployRequest(t, waitRouter(t, deployer, time.Minute), http.MethodPost, waitDeployPath+"?wait=true", ciBody, ciToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// A caller that hangs up ends the wait; the deploy itself carries on.
func TestDeployWait_EndsWhenTheCallerLeaves(t *testing.T) {
	deployer := ciDeployer()
	statusesThen(deployer, apphost.DeployStatusRolling)
	router := waitRouter(t, deployer, time.Hour)
	ctx, cancel := context.WithCancel(auth.SetToken(auth.SetUser(context.Background(), &domain.User{ID: "dev-1", Active: true}), ciToken))
	req := httptest.NewRequest(http.MethodPost, waitDeployPath+"?wait=true", strings.NewReader(ciBody)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait outlived its caller")
	}
}
