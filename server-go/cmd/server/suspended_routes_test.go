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
	"github.com/excalibase/provisioning-poc/internal/handler"
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
	// App hosting on and the optional handlers wired, so the routes the
	// default matrix leaves unmounted are walked too.
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, who, _, _ := buildMatrixPlatform(t, cfg, func(deps *handlerDeps, _ *fakePlatform) {
		instances := fakestore.NewInstances()
		instances.Create(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"})
		deps.documentsHandler = handler.NewDocumentBrowserHandler(nil)
		deps.storageHandler = handler.NewStorageHandler(nil, instances)
		deps.s3KeysHandler = handler.NewS3KeysHandler(nil, "")
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
	families := map[string]bool{}
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
		for _, family := range mountedFamilies {
			if strings.Contains(pattern, family) {
				families[family] = true
			}
		}
		return nil
	}
	if err := chi.Walk(suspended, walk); err != nil {
		t.Fatal(err)
	}
	if checked < len(projectRoutes) {
		t.Fatalf("walked only %d project writes; the router lost mounts", checked)
	}
	for _, family := range mountedFamilies {
		if !families[family] {
			t.Errorf("no write under %s was walked: the route family is unmounted in this test, so nothing pins it", family)
		}
	}
}

// mountedFamilies are the route families that sit behind app hosting or an
// optional handler. The default matrix leaves them unmounted, which once let a
// whole family go unchecked; a family missing from the walk fails the test.
var mountedFamilies = []string{
	"/apps/", "/app-network", "/app-templates", "/registry-credentials", "/domains",
	"/storage/", "/s3-keys", "/documentdb", "/metrics-keys", "/sites", "/functions", "/db-endpoint",
}

func TestSuspendedOwnersOrgStillReadsItsProject(t *testing.T) {
	router, token := suspendedMatrix(t, true)
	code, body := suspendedRequest(router, http.MethodGet, "/api/provision/"+matrixProjectA+"/", token)
	if strings.Contains(body, orgguard.Message) {
		t.Fatalf("a read was refused as suspended: %d %q", code, body)
	}
}

// GET /query runs one statement in a read-only transaction, so a suspended
// org's member can still read their data; POST /query, which can write, is refused.
func TestSuspendedOwnersOrgKeepsReadOnlySQLButNotWritableSQL(t *testing.T) {
	router, token := suspendedMatrix(t, true)
	_, body := suspendedRequest(router, http.MethodGet, "/api/schema/"+matrixProjectA+"/query?sql=select+1", token)
	if strings.Contains(body, orgguard.Message) {
		t.Fatalf("the read-only query was refused as suspended: %q", body)
	}
	code, body := suspendedRequest(router, http.MethodPost, "/api/schema/"+matrixProjectA+"/query", token)
	if code != http.StatusForbidden || !strings.Contains(body, orgguard.Message) {
		t.Fatalf("the writable query: %d %q, want it refused as suspended", code, body)
	}
}
