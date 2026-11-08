package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// realJSON is what the route writes: the real type, with fields no view names
// planted beside it. A view that passes the answer through shows them.
func realJSON(value any, planted map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	if planted == nil {
		return string(raw)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic(err)
	}
	for name, extra := range planted {
		fields[name] = extra
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(merged)
}

var plantedFields = map[string]any{"plantedSecret": "hunter2", "nested": map[string]any{"password": "hunter2"}}

func queuedRebuild() sitebuilds.Build {
	return sitebuilds.Build{
		ID: "b2", Kind: sitebuilds.KindSite, SiteID: "s1", ProjectID: testProjectA, Status: "QUEUED", Trigger: "rebuild",
		CommitSHA: "abc123", CommitMessage: "fix header", Repository: "acme/site", RequestedBy: "user-1",
		CreatedAt: time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC), Settings: sitebuilds.Settings{Env: map[string]string{"VITE_API": "https://api"}},
	}
}

func siteGitRouteCases() []routeCase {
	return []routeCase{
		{
			name: "disconnect_site_repo", tool: "disconnect_site_repo", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodDelete, projectsA+"/sites/s1/git", 204, ``) },
			expect: []string{"DELETE " + projectsA + "/sites/s1/git"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["disconnected"] != "s1" {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "rebuild_site", tool: "rebuild_site", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			setup: func(f *fakeRoutes) {
				build := queuedRebuild()
				f.on(http.MethodPost, projectsA+"/sites/s1/builds", 202, realJSON(build, plantedFields))
			},
			expect: []string{"POST " + projectsA + "/sites/s1/builds"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				build, _ := out["build"].(map[string]any)
				if build["id"] != "b2" || build["status"] != "QUEUED" || build["commitSha"] != "abc123" || build["trigger"] != "rebuild" {
					t.Fatalf("build = %v", build)
				}
				for _, hidden := range []string{"plantedSecret", "nested", "settings", "log", "requestedBy"} {
					if _, ok := build[hidden]; ok {
						t.Errorf("build carries %s: %v", hidden, build)
					}
				}
			},
		},
		{
			name: "list_site_build_secrets", tool: "list_site_build_secrets", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/build-secrets", 200,
					realJSON([]sitebuilds.SecretVisibility{{Name: "STRIPE_KEY"}, {Name: "PUBLIC_API", BuildVisible: true}}, nil))
			},
			expect: []string{"GET " + projectsA + "/sites/build-secrets"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				secrets, _ := out["secrets"].([]any)
				if len(secrets) != 2 {
					t.Fatalf("secrets = %v", out)
				}
				if first, _ := secrets[1].(map[string]any); first["name"] != "PUBLIC_API" || first["buildVisible"] != true || len(first) != 2 {
					t.Errorf("secret = %v", secrets[1])
				}
			},
		},
	}
}

// Single-host answers 503 with its reason, git builds off answer 404: both are explained.
func TestSiteGitWritesExplainTheirRefusals(t *testing.T) {
	for _, tool := range []struct {
		name   string
		method string
		path   string
	}{
		{"disconnect_site_repo", http.MethodDelete, projectsA + "/sites/s1/git"},
		{"rebuild_site", http.MethodPost, projectsA + "/sites/s1/builds"},
	} {
		f := newFakeRoutes()
		f.on(tool.method, tool.path, 503, `{"error":"git builds are not offered on single-host installs yet"}`)
		res := callTool(t, session(t, f, &recordingAudit{}, writeCaller()), tool.name, map[string]any{"project_id": testProjectA, "site_id": "s1"})
		if !res.IsError || !strings.Contains(resultText(res), "single-host") {
			t.Errorf("%s: %s", tool.name, resultText(res))
		}
	}
}

func TestSiteGitToolsRefuseAnIDThatNamesNoSite(t *testing.T) {
	f := newFakeRoutes()
	cs := session(t, f, &recordingAudit{}, writeCaller())
	for _, tool := range []string{"disconnect_site_repo", "rebuild_site"} {
		for _, id := range []string{"", ".."} {
			if res := callTool(t, cs, tool, map[string]any{"project_id": testProjectA, "site_id": id}); !res.IsError {
				t.Errorf("%s accepted %q", tool, id)
			}
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v", f.calls)
	}
}
