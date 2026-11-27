package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/trust"
	"github.com/go-chi/chi/v5"
)

func TestContainerWriteClassification(t *testing.T) {
	const base = "/api/projects/p1"
	cases := []struct {
		method, path string
		gated        bool
	}{
		{http.MethodPost, base + "/apps/", true},
		{http.MethodPatch, base + "/apps/a1/", true},
		{http.MethodPost, base + "/apps/a1/deploy", true},
		{http.MethodPost, base + "/apps/a1/deploys/d1/redeploy", true},
		{http.MethodPost, base + "/apps/a1/builds", true},
		{http.MethodPost, base + "/apps/a1/resume", true},
		{http.MethodPut, base + "/apps/a1/git", true},
		{http.MethodPost, base + "/apps/a1/anything-new", true},
		{http.MethodPost, base + "/app-templates/t1/deploy", true},
		{http.MethodPost, base + "/sites/s1/builds", true},
		{http.MethodPost, base + "/functions/", true},
		{http.MethodPost, "/api/schema/p1/functions", false},
		// Anchored: the allow-list and the container list only read a project route.
		{http.MethodPost, "/api/projects/p1/apps/a1/deploy/apps/a2/pause", true},
		{http.MethodPost, "/other/apps/a1/pause", false},
		{http.MethodPost, "/x/api/projects/p1/sites/s1/builds", false},
		// An escaped slash stays inside its segment, as the router reads it.
		{http.MethodPost, base + "/apps/a1%2Fpause", true},
		{http.MethodPost, base + "/apps/a1%2Fx/pause", false},
		{http.MethodPost, base + "/apps/x%2Fdomains/deploy", true},
		{http.MethodPut, base + "/functions/egress", false},
		{http.MethodPost, base + "/functions/secrets", false},
		{http.MethodPut, base + "/sites/s1/git", true},
		{http.MethodDelete, base + "/apps/a1/", false},
		{http.MethodPost, base + "/apps/a1/pause", false},
		{http.MethodPut, base + "/apps/a1/secrets/KEY", false},
		{http.MethodPost, base + "/apps/a1/domains/", false},
		{http.MethodPost, base + "/apps/a1/disk", false},
		{http.MethodPost, base + "/apps/a1/git/sign-in", false},
		{http.MethodGet, base + "/apps/a1/deploys", false},
		{http.MethodPost, base + "/sites/s1/deploy", false},
		{http.MethodPost, "/api/provision/p1/database", false},
		{http.MethodPost, "/api/schema/p1/tables", false},
	}
	for _, tc := range cases {
		if got := ContainerWrite(tc.method, tc.path); got != tc.gated {
			t.Errorf("%s %s: gated=%v, want %v", tc.method, tc.path, got, tc.gated)
		}
	}
}

type stubTrust struct{ state trust.State }

func (s stubTrust) ForProject(context.Context, string) (trust.State, error) { return s.state, nil }

func gateRouter(state trust.State) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}", func(r chi.Router) {
		r.Use(RequireVerifiedForContainers(trust.NewGate(stubTrust{state}, trust.Policy{MinAgeDays: 90})))
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	return r
}

func gateStatus(h http.Handler, method, path string) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code, w.Body.String()
}

func TestRequireVerifiedForContainersRefusesALimitedOwner(t *testing.T) {
	limited := gateRouter(trust.Limited)
	code, body := gateStatus(limited, http.MethodPost, "/api/projects/p1/apps/a1/deploy")
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"trust_required"`) {
		t.Fatalf("deploy by a limited owner: %d %s", code, body)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/projects/p1/apps/"},
		{http.MethodDelete, "/api/projects/p1/apps/a1/"},
		{http.MethodPost, "/api/projects/p1/apps/a1/pause"},
		{http.MethodPost, "/api/projects/p1/sites/s1/deploy"},
	} {
		if code, _ := gateStatus(limited, tc.method, tc.path); code != http.StatusNoContent {
			t.Errorf("%s %s by a limited owner: %d, want it to pass", tc.method, tc.path, code)
		}
	}
	if code, _ := gateStatus(gateRouter(trust.Full), http.MethodPost, "/api/projects/p1/apps/a1/deploy"); code != http.StatusNoContent {
		t.Errorf("deploy by a full owner: %d", code)
	}
}

func TestRequireVerifiedForContainersWithNoGateAllowsAll(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}", func(r chi.Router) {
		r.Use(RequireVerifiedForContainers(nil))
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	if code, _ := gateStatus(r, http.MethodPost, "/api/projects/p1/apps/"); code != http.StatusNoContent {
		t.Fatalf("nil gate: %d", code)
	}
}

// The gate reads the escaped path the router matches, not the decoded one.
func TestRequireVerifiedForContainersReadsTheEscapedPath(t *testing.T) {
	limited := gateRouter(trust.Limited)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/apps/a1%2Fx/deploy", nil)
	w := httptest.NewRecorder()
	limited.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("an escaped-slash deploy path: %d, want 403", w.Code)
	}
}

// Decoded, this path reads as the allowed /apps/x/domains/deploy; only the
// escaped form shows it is a deploy of an app named "x/domains".
func TestRequireVerifiedForContainersRefusesAPathThatOnlyLooksAllowedWhenDecoded(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/apps/x%2Fdomains/deploy", nil)
	w := httptest.NewRecorder()
	gateRouter(trust.Limited).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a path that only looks allowed once decoded: %d, want 403", w.Code)
	}
}
