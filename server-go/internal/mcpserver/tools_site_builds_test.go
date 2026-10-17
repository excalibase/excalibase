package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

func siteBuildRouteCases() []routeCase {
	return []routeCase{
		{
			name: "connect_site_repo", tool: "connect_site_repo",
			args: map[string]any{"project_id": testProjectA, "site_id": "s1", "repository": "acme/site", "build_env": map[string]any{"VITE_API": "https://api"}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, projectsA+"/sites/s1/git", 200, `{"connection":{"repository":"acme/site"},"build":{"id":"b1","status":"QUEUED"}}`)
			},
			expect: []string{"PUT " + projectsA + "/sites/s1/git"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				for _, want := range []string{`"repository":"acme/site"`, `"outputDir":"dist"`, `"VITE_API":"https://api"`} {
					if !strings.Contains(calls[0].Body, want) {
						t.Errorf("body %s lacks %s", calls[0].Body, want)
					}
				}
				if out["build"] == nil || out["next"] == nil {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "connect_site_repo keeps the current variables", tool: "connect_site_repo",
			args: map[string]any{"project_id": testProjectA, "site_id": "s1", "repository": "acme/site"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/git", 200, `{"connected":true,"connection":{"settings":{"env":{"KEPT":"1"}}}}`)
				f.on(http.MethodPut, projectsA+"/sites/s1/git", 200, `{"connection":{}}`)
			},
			expect: []string{"GET " + projectsA + "/sites/s1/git", "PUT " + projectsA + "/sites/s1/git"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[1].Body, `"KEPT":"1"`) {
					t.Errorf("variables not kept: %s", calls[1].Body)
				}
			},
		},
		{
			name: "list_site_builds", tool: "list_site_builds", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/builds", 200, `[{"id":"b1","status":"FAILED"}]`)
			},
			expect: []string{"GET " + projectsA + "/sites/s1/builds"},
		},
		{
			name: "list_site_builds one", tool: "list_site_builds", args: map[string]any{"project_id": testProjectA, "site_id": "s1", "build_id": "b1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/builds/b1", 200, `{"id":"b1","log":"== build =="}`)
			},
			expect: []string{"GET " + projectsA + "/sites/s1/builds/b1"},
		},
	}
}

// Without a GitHub sign-in the tool sends the user to Studio, and starts no sign-in it could hand to someone else.
func TestConnectSiteRepo_SendsTheUserToStudioToSignIn(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodPut, projectsA+"/sites/s1/git", 403, `{"error":"sign in to GitHub from this site first"}`)
	cs := session(t, f, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "connect_site_repo", map[string]any{"project_id": testProjectA, "site_id": "s1", "repository": "acme/site",
		"build_env": map[string]any{}})
	if !res.IsError || !strings.Contains(resultText(res), "Sign in with GitHub") || len(f.calls) != 1 {
		t.Fatalf("result = %s, calls %d", resultText(res), len(f.calls))
	}
}

// Single-host says why, in the server's own words.
func TestListSiteBuilds_SingleHost(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodGet, projectsA+"/sites/s1/builds", 503, `{"error":"git builds are not offered on single-host installs yet"}`)
	cs := session(t, f, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "list_site_builds", map[string]any{"project_id": testProjectA, "site_id": "s1"})
	if !res.IsError || !strings.Contains(resultText(res), "single-host") {
		t.Fatalf("result = %s", resultText(res))
	}
}
