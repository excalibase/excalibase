package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
)

func appLifecyclePath(op string) string {
	return "/api/projects/" + deployHandlerProject + "/apps/app-1/" + op
}

func TestAppLifecycleHandler_PauseAndResumeAnswerWithTheStatus(t *testing.T) {
	for op, want := range map[string]string{"pause": apphost.StatusStopped, "resume": apphost.StatusRunning} {
		deployer := newFakeAppDeployer()
		rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, appLifecyclePath(op))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body=%s", op, rec.Code, rec.Body.String())
		}
		var got struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ID != "app-1" || got.Status != want {
			t.Errorf("%s: body %+v", op, got)
		}
		if deployer.lifecycleCalls[0] != op+":"+deployHandlerProject+"/app-1" {
			t.Errorf("%s: called %v", op, deployer.lifecycleCalls)
		}
	}
}

func TestAppLifecycleHandler_DeleteIsNoContent(t *testing.T) {
	deployer := newFakeAppDeployer()
	rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodDelete, appLifecyclePath(""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAppLifecycleHandler_MapsTheRefusals(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{apphost.ErrAppNotFound, http.StatusNotFound},
		{fmt.Errorf("%w: it is PAUSING", apphost.ErrAppStatusConflict), http.StatusConflict},
		{fmt.Errorf("%w: it is DELETING", apphost.ErrAppBusy), http.StatusConflict},
		{service.ErrProjectOperationRunning, http.StatusConflict},
		{k8s.ErrAppNotDeployed, http.StatusConflict},
		{k8s.ErrAppNotPaused, http.StatusConflict},
		{fmt.Errorf("%w after 3m0s: web-1", k8s.ErrAppPodsRemain), http.StatusGatewayTimeout},
		{fmt.Errorf("%w: web did not become ready within 5m0s", k8s.ErrAppRollout), http.StatusBadGateway},
		{errors.New("pq: connection reset"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		for _, request := range []struct{ method, op string }{
			{http.MethodPost, "pause"}, {http.MethodPost, "resume"}, {http.MethodDelete, ""},
		} {
			deployer := newFakeAppDeployer()
			deployer.lifecycleErr = tc.err
			rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), request.method, appLifecyclePath(request.op))
			if rec.Code != tc.code {
				t.Errorf("%s %q with %v: got %d want %d", request.method, request.op, tc.err, rec.Code, tc.code)
			}
			if strings.Contains(rec.Body.String(), "pq:") {
				t.Errorf("storage detail leaked: %s", rec.Body.String())
			}
		}
	}
}

func TestAppLifecycleHandler_RejectsAnInvalidAppID(t *testing.T) {
	deployer := newFakeAppDeployer()
	rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost,
		"/api/projects/"+deployHandlerProject+"/apps/not%20valid/pause")
	if rec.Code != http.StatusBadRequest || len(deployer.lifecycleCalls) != 0 {
		t.Fatalf("status %d, calls %v", rec.Code, deployer.lifecycleCalls)
	}
}
