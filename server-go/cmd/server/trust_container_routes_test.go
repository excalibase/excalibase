package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/trust"
	"github.com/go-chi/chi/v5"
)

// EXC-899: a limited (unverified) account must not start, build or scale a
// container. The gate is one middleware on every project mount; this walks the
// real router so a container route mounted without it, or a new route that looks
// like one and nobody decided about, fails here. Databases are not gated.

// containerWrites is every project write that runs tenant code in a container,
// decided here and not derived from the code under test.
var containerWrites = map[string]bool{
	"POST /api/projects/{projectId}/apps/":                                    true,
	"PATCH /api/projects/{projectId}/apps/{appId}/":                           true,
	"POST /api/projects/{projectId}/apps/{appId}/builds":                      true,
	"POST /api/projects/{projectId}/apps/{appId}/deploy":                      true,
	"POST /api/projects/{projectId}/apps/{appId}/deploys/{deployId}/redeploy": true,
	"POST /api/projects/{projectId}/apps/{appId}/resume":                      true,
	"PUT /api/projects/{projectId}/apps/{appId}/git":                          true,
	"POST /api/projects/{projectId}/app-templates/{templateId}/deploy":        true,
	"POST /api/projects/{projectId}/sites/{siteId}/builds":                    true,
	"POST /api/projects/{projectId}/functions/":                               true,
	"PUT /api/projects/{projectId}/sites/{siteId}/git":                        true,
}

const reasonRemoving = "removing starts nothing"

// decidedNotContainer are the writes that look like container work by name but
// start nothing: they stop, delete, configure or serve static files.
var decidedNotContainer = map[string]string{
	"DELETE /api/projects/{projectId}/apps/{appId}/":                            reasonRemoving,
	"POST /api/projects/{projectId}/apps/{appId}/pause":                         "stopping is what a limit wants",
	"POST /api/projects/{projectId}/apps/{appId}/disk":                          "a disk is storage, not compute",
	"PUT /api/projects/{projectId}/apps/{appId}/secrets/{name}":                 "a variable starts nothing",
	"POST /api/projects/{projectId}/apps/{appId}/domains/":                      "a hostname starts nothing",
	"DELETE /api/projects/{projectId}/apps/{appId}/domains/{domainId}":          reasonRemoving,
	"POST /api/projects/{projectId}/apps/{appId}/domains/{domainId}/verify":     "a DNS check starts nothing",
	"DELETE /api/projects/{projectId}/apps/{appId}/git":                         "disconnecting starts nothing",
	"POST /api/projects/{projectId}/apps/{appId}/git/sign-in":                   "a sign-in starts nothing",
	"PUT /api/projects/{projectId}/app-network/":                                "network settings start nothing",
	"DELETE /api/projects/{projectId}/sites/{siteId}/":                          reasonRemoving,
	"PATCH /api/projects/{projectId}/sites/{siteId}/":                           "site settings start nothing",
	"POST /api/projects/{projectId}/sites/":                                     "a site record starts nothing",
	"POST /api/projects/{projectId}/sites/{siteId}/deploy":                      "gated by the sites service when the site is a server site (it publishes customer code as a Worker); a static site runs no code",
	"POST /api/projects/{projectId}/sites/{siteId}/deploys/{deployId}/rollback": "gated by the sites service when the site is a server site (it republishes old customer code); a static site runs no code",
	"DELETE /api/projects/{projectId}/sites/{siteId}/git":                       "disconnecting starts nothing",
	"POST /api/projects/{projectId}/sites/{siteId}/git/sign-in":                 "a sign-in starts nothing",
	"POST /api/projects/{projectId}/functions/{fnId}/invoke":                    "runs in the project's already deployed function runtime; its deploy is gated",
	"PUT /api/projects/{projectId}/functions/egress":                            "a hostname list starts nothing",
	"POST /api/projects/{projectId}/functions/secrets":                          "a secret starts nothing",
	"DELETE /api/projects/{projectId}/functions/secrets/{key}":                  reasonRemoving,
	"DELETE /api/projects/{projectId}/functions/{fnId}/":                        reasonRemoving,
	"POST /api/provision/{projectId}/resume":                                    "resumes a database",
}

// looksLikeContainerWork picks the routes the walk makes someone decide.
func looksLikeContainerWork(pattern string) bool {
	if strings.HasPrefix(pattern, "/api/projects/{projectId}/functions") {
		return true
	}
	for _, word := range []string{"/apps/", "/app-templates", "/builds", "/deploy", "/redeploy", "/resume", "/scale", "/git", "/containers", "/workers"} {
		if strings.Contains(pattern, word) {
			return true
		}
	}
	return false
}

// stubTrust answers one owner's state for every project.
type stubTrust struct{ state trust.State }

func (s stubTrust) ForProject(context.Context, string) (trust.State, error) { return s.state, nil }

func trustMatrix(t *testing.T, state trust.State) (*chi.Mux, string) {
	t.Helper()
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, who, _, _ := buildMatrixPlatform(t, cfg, func(deps *handlerDeps, _ *fakePlatform) {
		instances := fakestore.NewInstances()
		instances.Create(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"})
		deps.documentsHandler = handler.NewDocumentBrowserHandler(nil)
		deps.storageHandler = handler.NewStorageHandler(nil, instances)
		deps.s3KeysHandler = handler.NewS3KeysHandler(nil, "")
		deps.trustGate = trust.NewGate(stubTrust{state}, trust.Policy{MinAgeDays: 90})
	})
	mux, ok := router.(*chi.Mux)
	if !ok {
		t.Fatal("buildRouter must return a *chi.Mux")
	}
	return mux, who[callerDeveloper]
}

func refusedAsUnverified(code int, body string) bool {
	return code == http.StatusForbidden && strings.Contains(body, `"code":"trust_required"`)
}

func TestEveryContainerWriteRefusesALimitedAccount(t *testing.T) {
	limited, token := trustMatrix(t, trust.Limited)
	full, _ := trustMatrix(t, trust.Full)
	seen := map[string]bool{}
	walk := func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !isProjectWrite(method, pattern) {
			return nil
		}
		key := method + " " + pattern
		seen[key] = true
		path := concretePath(pattern)
		code, body := suspendedRequest(limited, method, path, token)
		_, decided := decidedNotContainer[key]
		switch {
		case containerWrites[key] && !refusedAsUnverified(code, body):
			t.Errorf("%s: a limited account got %d %q, want 403 trust_required", key, code, body)
		case !containerWrites[key] && refusedAsUnverified(code, body):
			t.Errorf("%s: refused as unverified but not listed as container work", key)
		case !containerWrites[key] && !decided && looksLikeContainerWork(pattern):
			t.Errorf("%s: looks like container work and nobody decided: gate it and list it in containerWrites, or list it in decidedNotContainer with the reason", key)
		}
		if code, body := suspendedRequest(full, method, path, token); refusedAsUnverified(code, body) {
			t.Errorf("%s: refused a verified account: %d %q", key, code, body)
		}
		return nil
	}
	if err := chi.Walk(limited, walk); err != nil {
		t.Fatal(err)
	}
	for key := range containerWrites {
		if !seen[key] {
			t.Errorf("%s is listed as container work but the router has no such route", key)
		}
	}
	for key := range decidedNotContainer {
		if !seen[key] {
			t.Errorf("%s is listed as decided but the router has no such route", key)
		}
	}
}

// Databases are not gated: a limited account still provisions and queries them.
func TestALimitedAccountKeepsItsDatabase(t *testing.T) {
	router, token := trustMatrix(t, trust.Limited)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/provision/" + matrixProjectA + "/backup/trigger"},
		{http.MethodPost, "/api/schema/" + matrixProjectA + "/tables"},
		{http.MethodPost, "/api/provision/" + matrixProjectA + "/resume"},
	} {
		if code, body := suspendedRequest(router, tc.method, tc.path, token); refusedAsUnverified(code, body) {
			t.Errorf("%s %s refused as unverified: a database is not a container", tc.method, tc.path)
		}
	}
}
