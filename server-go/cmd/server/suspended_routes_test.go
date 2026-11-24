package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// EXC-845: a member of a suspended owner's org must not change anything on
// the project, whichever handler the route reaches. The gate is one middleware
// on every project mount; this walks the real router so a route mounted
// without it, or a new write that nobody decided about, fails here.

// suspendedMayWrite is the whole list of writes a suspended org's member keeps,
// decided once and kept here beside the test, not derived from the code under test.
//   - any DELETE: removing a project, app, site, function or key starts and
//     exposes nothing, and lets the member clean up;
//   - POST .../pause: stopping compute is what the suspension wants.
func suspendedMayWrite(method, path string) bool {
	return method == http.MethodDelete || (method == http.MethodPost && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/pause"))
}

func suspendedMatrix(t *testing.T, suspended bool) (*chi.Mux, string) {
	t.Helper()
	router, who, _, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, _ *fakePlatform) {
		instances := fakestore.NewInstances()
		instances.Create(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"})
		deps.orgGuard = orgguard.New(instances, func(context.Context) (map[string]bool, error) {
			if suspended {
				return map[string]bool{matrixOrgA: true}, nil
			}
			return map[string]bool{}, nil
		})
	})
	mux, ok := router.(*chi.Mux)
	if !ok {
		t.Fatal("buildRouter must return a *chi.Mux")
	}
	return mux, who[callerDeveloper]
}

func suspendedRequest(router http.Handler, method, path, token string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Body)
	return w.Code, string(body)
}

func TestEveryProjectWriteRefusesASuspendedOwnersOrg(t *testing.T) {
	suspended, token := suspendedMatrix(t, true)
	unsuspended, _ := suspendedMatrix(t, false)
	checked := 0
	walk := func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(pattern, "{projectId}") || isPublicProjectRoute(pattern) || !isMutating(method) {
			return nil
		}
		path := concretePath(pattern)
		code, body := suspendedRequest(suspended, method, path, token)
		refused := code == http.StatusForbidden && strings.Contains(body, orgguard.Message)
		switch {
		case suspendedMayWrite(method, path) && refused:
			t.Errorf("%s %s: refused a write the suspended may make", method, pattern)
		case !suspendedMayWrite(method, path) && !refused:
			t.Errorf("%s %s: got %d %q for a suspended owner's org, want 403 %q", method, pattern, code, body, orgguard.Message)
		}
		// The same request to an org in good standing must not meet the refusal,
		// or the 403 above says nothing about the gate.
		if code, body := suspendedRequest(unsuspended, method, path, token); strings.Contains(body, orgguard.Message) {
			t.Errorf("%s %s: refused an org in good standing: %d %q", method, pattern, code, body)
		}
		checked++
		return nil
	}
	if err := chi.Walk(suspended, walk); err != nil {
		t.Fatal(err)
	}
	if checked < len(projectRoutes) {
		t.Fatalf("walked only %d project writes; the router lost mounts", checked)
	}
}

func TestSuspendedOwnersOrgStillReadsItsProject(t *testing.T) {
	router, token := suspendedMatrix(t, true)
	code, body := suspendedRequest(router, http.MethodGet, "/api/provision/"+matrixProjectA+"/", token)
	if strings.Contains(body, orgguard.Message) {
		t.Fatalf("a read was refused as suspended: %d %q", code, body)
	}
}
