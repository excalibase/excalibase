package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

const deployTokenRefusal = `{"error":"a deploy token may only deploy its own site and read its deploys"}`

// EXC-620, through the real router: a deploy token reaches only its own
// site's deploy routes, never another site, another project, or anything a
// session cookie beside it could reach.
func TestDeployTokenStaysOnItsOwnSiteThroughTheRouter(t *testing.T) {
	harness, principals := policyRouter(t)
	owner := principalNamed(t, principals, "orgOwner")
	send := func(method, path string, withSession bool) (int, string) {
		harness.reseed()
		request := httptest.NewRequest(method, path, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+policyDeployToken)
		if withSession {
			request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: owner.token})
		}
		recorder := httptest.NewRecorder()
		harness.router.ServeHTTP(recorder, request)
		return recorder.Code, strings.TrimSpace(recorder.Body.String())
	}
	for _, call := range []struct {
		method, path string
		session      bool
	}{
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/sites/other/deploy", false},
		{http.MethodGet, "/api/projects/" + matrixProjectA + "/sites/other/deploys", false},
		{http.MethodPost, "/api/projects/" + matrixProjectB + "/sites/x/deploy", false},
		{http.MethodPost, "/api/projects/" + matrixProjectA + "/sites/x/deploys/d1/rollback", true},
		{http.MethodGet, "/api/auth/me", true},
		{http.MethodDelete, "/api/provision/" + matrixProjectA, true},
	} {
		if code, body := send(call.method, call.path, call.session); code != http.StatusForbidden || body != deployTokenRefusal {
			t.Errorf("%s %s: %d %s, want the deploy token refused", call.method, call.path, code, body)
		}
	}
	if _, body := send(http.MethodPost, "/api/projects/"+matrixProjectA+"/sites/x/deploy", true); isGateRefusal(body) {
		t.Fatalf("its own site's deploy was refused at a gate: %s", body)
	}
}
