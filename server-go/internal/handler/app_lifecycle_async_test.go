package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

func doLifecycleRequest(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Prefer", "respond-async")
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "dev-1", Active: true}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// EXC-523: Studio prefers not to wait for a pause, resume or deletion, which
// may queue for minutes behind the app's lease. The answer is 202 with the
// app as it stands and when the request was accepted, so a failure recorded
// on the app afterwards can be told from an older one.
func TestAppLifecycle_PreferRespondAsyncAnswers202(t *testing.T) {
	base := "/api/projects/" + deployHandlerProject + "/apps/app-1"
	for _, tc := range []struct {
		name, method, path, body, call, status string
	}{
		{"pause", http.MethodPost, base + "/pause", "", "pause-async", apphost.StatusRunning},
		{"resume", http.MethodPost, base + "/resume", "", "resume-async", apphost.StatusStopped},
		{"delete", http.MethodDelete, base + "/", `{"confirmDeleteDisk":true}`, "delete-async-confirmed", apphost.StatusRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployer := newFakeAppDeployer()
			before := time.Now().UTC().Add(-time.Second)
			rec := doLifecycleRequest(setupAppDeployRouter(t, deployer), tc.method, tc.path, tc.body)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status %d, want 202; body %s", rec.Code, rec.Body.String())
			}
			var got struct {
				ID         string    `json:"id"`
				Status     string    `json:"status"`
				AcceptedAt time.Time `json:"acceptedAt"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.ID != "app-1" || got.Status != tc.status || got.AcceptedAt.Before(before) {
				t.Fatalf("answer %+v, want the app as it stands and when it was accepted", got)
			}
			if len(deployer.lifecycleCalls) != 1 || !strings.HasPrefix(deployer.lifecycleCalls[0], tc.call+":") {
				t.Fatalf("calls %v, want %s", deployer.lifecycleCalls, tc.call)
			}
		})
	}
}

// Refusals that need no lease are still answered in the response.
func TestAppLifecycle_PreferRespondAsyncStillAnswersARefusal(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.lifecycleErr = service.ErrAppDiskDeleteUnconfirmed
	rec := doLifecycleRequest(setupAppDeployRouter(t, deployer), http.MethodDelete,
		"/api/projects/"+deployHandlerProject+"/apps/app-1/", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	deployer.lifecycleErr = apphost.ErrAppNotFound
	rec = doLifecycleRequest(setupAppDeployRouter(t, deployer), http.MethodPost,
		"/api/projects/"+deployHandlerProject+"/apps/app-1/pause", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
}
