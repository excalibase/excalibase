package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

const (
	corsStoreOrigin = "https://store.example.com"
	corsOtherOrigin = "https://evil.example.net"
)

type fakeCorsStore struct {
	origins map[string][]string
	err     error
}

func (s *fakeCorsStore) GetCorsOrigins(_ context.Context, projectID string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.origins[projectID], nil
}

func (s *fakeCorsStore) SetCorsOrigins(_ context.Context, projectID string, origins []string) error {
	s.origins[projectID] = origins
	return nil
}

func corsTestRouter(t *testing.T, cors *fakeCorsStore) *chi.Mux {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	if cors != nil {
		h.SetCorsStore(cors)
	}
	open := false
	store.Save(&edgefn.Function{ProjectID: "proj_p1", ID: "hello", Name: "Hello", VerifyJwt: &open, Active: true,
		Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}}})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__hello", Code: "ok"})
	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	return r
}

func corsCall(r *chi.Mux, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/functions/v1/proj_p1/hello", nil)
	req.Header.Set("Origin", origin)
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-excalibase-publishable-key")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A browser app calls its project's functions from an origin the project
// allows, the same allowlist its GraphQL and REST use (EXC-518, EXC-23).
func TestPublicInvoke_CORSFollowsTheProjectAllowlist(t *testing.T) {
	r := corsTestRouter(t, &fakeCorsStore{origins: map[string][]string{"proj_p1": {corsStoreOrigin}}})

	pre := corsCall(r, http.MethodOptions, corsStoreOrigin)
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != corsStoreOrigin {
		t.Fatalf("preflight from an allowed origin: %d ACAO=%q", pre.Code, pre.Header().Get("Access-Control-Allow-Origin"))
	}
	allowed := strings.ToLower(pre.Header().Get("Access-Control-Allow-Headers"))
	for _, header := range []string{"authorization", "content-type", "x-excalibase-publishable-key", "x-excalibase-role"} {
		if !strings.Contains(allowed, header) {
			t.Errorf("Allow-Headers %q is missing %s", allowed, header)
		}
	}
	if pre.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("functions never take cookies cross-origin")
	}
	if got := corsCall(r, http.MethodPost, corsStoreOrigin).Header().Get("Access-Control-Allow-Origin"); got != corsStoreOrigin {
		t.Fatalf("actual request from an allowed origin: ACAO=%q", got)
	}

	for _, method := range []string{http.MethodOptions, http.MethodPost} {
		if got := corsCall(r, method, corsOtherOrigin).Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s from an origin the project does not list: ACAO=%q", method, got)
		}
	}
}

func TestPublicInvoke_CORSWildcardProject(t *testing.T) {
	r := corsTestRouter(t, &fakeCorsStore{origins: map[string][]string{"proj_p1": {domain.CorsWildcard}}})
	if got := corsCall(r, http.MethodOptions, corsOtherOrigin).Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("wildcard project: ACAO=%q", got)
	}
}

func TestPublicInvoke_CORSFailsClosed(t *testing.T) {
	for name, cors := range map[string]*fakeCorsStore{
		"no allowlist store": nil,
		"store unreadable":   {err: errors.New("db down")},
		"project unset":      {origins: map[string][]string{}},
	} {
		r := corsTestRouter(t, cors)
		if got := corsCall(r, http.MethodOptions, corsStoreOrigin).Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: ACAO=%q, want none", name, got)
		}
	}
}

// EXC-563: one rule with the engine and auth. A preflight from an unlisted
// origin is refused (403, no CORS headers); an actual request is served for any
// Origin and granted only when listed. Functions take a bearer token, never a
// cookie, so Origin alone is no reason to refuse a call, and native apps
// (Capacitor, Tauri, Electron file pages sending null) or server-side proxies
// must reach the function.
func TestPublicInvoke_CORSRefusesUnlistedPreflightWith403(t *testing.T) {
	r := corsTestRouter(t, &fakeCorsStore{origins: map[string][]string{"proj_p1": {corsStoreOrigin}}})

	for _, origin := range []string{corsOtherOrigin, "capacitor://localhost", "null"} {
		pre := corsCall(r, http.MethodOptions, origin)
		if pre.Code != http.StatusForbidden {
			t.Errorf("%s: preflight status %d, want 403", origin, pre.Code)
		}
		for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
			if got := pre.Header().Get(header); got != "" {
				t.Errorf("%s: refused preflight carries %s=%q", origin, header, got)
			}
		}
		if got := pre.Header().Get("Vary"); got != "Origin" {
			t.Errorf("%s: Vary=%q, want Origin", origin, got)
		}
	}
}

func TestPublicInvoke_CORSServesUnlistedOriginsWithoutGrant(t *testing.T) {
	r := corsTestRouter(t, &fakeCorsStore{origins: map[string][]string{"proj_p1": {corsStoreOrigin}}})

	for _, origin := range []string{corsOtherOrigin, "capacitor://localhost", "tauri://localhost", "null", "http://localhost:5173"} {
		w := corsCall(r, http.MethodPost, origin)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status %d, want the function served", origin, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: ACAO=%q, want none", origin, got)
		}
		if got := w.Header().Get("Vary"); got != "Origin" {
			t.Errorf("%s: Vary=%q, want Origin", origin, got)
		}
	}
}

func TestPublicInvoke_CORSGrantsAListedNativeAppOrigin(t *testing.T) {
	r := corsTestRouter(t, &fakeCorsStore{origins: map[string][]string{"proj_p1": {"capacitor://localhost"}}})

	pre := corsCall(r, http.MethodOptions, "capacitor://localhost")
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "capacitor://localhost" {
		t.Fatalf("listed native origin: %d ACAO=%q", pre.Code, pre.Header().Get("Access-Control-Allow-Origin"))
	}
}
