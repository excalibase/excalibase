package mcpserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

var appNow = time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)

func redeployedDeploy() apphost.Deploy {
	return apphost.Deploy{
		ID: "d2", AppID: "web", ProjectID: testProjectA, Revision: 4, Image: "ghcr.io/a/web@sha256:abc", Digest: "sha256:abc", Source: "mcp",
		CommitSHA: "abc1234", RedeployOf: "d1", Kind: "redeploy", Status: "pending", CreatedBy: "user-1", CreatedAt: appNow,
		Spec: apphost.DeploySpec{Image: "ghcr.io/a/web@sha256:abc", Port: 3000, Replicas: 1, URL: "https://web.example.test",
			Env: []apphost.EnvSummary{{Name: "API_KEY", Kind: "secret"}}},
	}
}

func appDomain() service.DomainView {
	return service.DomainView{
		Domain: apphost.Domain{ID: "dom1", ProjectID: testProjectA, AppID: "web", Hostname: "www.example.com", Status: apphost.DomainPending,
			CreatedAt: appNow}, CNAMETarget: "web-proj-a.apps.example.test",
	}
}

func appBuild() sitebuilds.Build {
	return sitebuilds.Build{
		ID: "ab1", Kind: sitebuilds.KindApp, AppID: "web", ProjectID: testProjectA, Status: "QUEUED", Trigger: "rebuild",
		CommitSHA: "abc123", Repository: "acme/web", RequestedBy: "user-1", CreatedAt: appNow,
		App: &sitebuilds.AppBuild{AppSettings: sitebuilds.AppSettings{Builder: "dockerfile", BuildArgs: map[string]string{"TOKEN": "plain"}}},
	}
}

func appMoreRouteCases() []routeCase {
	gitOn := []features.Feature{features.AppGitBuilds}
	return []routeCase{
		{
			name: "set_app_secret", tool: "set_app_secret", args: map[string]any{"project_id": testProjectA, "app_id": "web", "name": "API_KEY", "value": "s3cr3t-value"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, projectsA+"/apps/web/secrets/API_KEY", 200, `{"name":"API_KEY","set":true}`)
			},
			expect: []string{"PUT " + projectsA + "/apps/web/secrets/API_KEY"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"value":"s3cr3t-value"}` {
					t.Errorf("body = %s", calls[0].Body)
				}
				if out["name"] != "API_KEY" || out["set"] != true || strings.Contains(realJSON(out, nil), "s3cr3t") {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "redeploy_app", tool: "redeploy_app", args: map[string]any{"project_id": testProjectA, "app_id": "web", "deploy_id": "d1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/apps/web/deploys/d1/redeploy", 202, realJSON(redeployedDeploy(), plantedFields))
			},
			expect: []string{"POST " + projectsA + "/apps/web/deploys/d1/redeploy"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				deploy, _ := out["deploy"].(map[string]any)
				if deploy["id"] != "d2" || deploy["redeployOf"] != "d1" || deploy["status"] != "pending" || deploy["url"] != "https://web.example.test" {
					t.Fatalf("deploy = %v", deploy)
				}
				for _, hidden := range []string{"plantedSecret", "nested", "spec", "createdBy"} {
					if _, ok := deploy[hidden]; ok {
						t.Errorf("deploy carries %s: %v", hidden, deploy)
					}
				}
			},
		},
		{
			name: "list_app_domains", tool: "list_app_domains", args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/domains/", 200, "["+realJSON(appDomain(), plantedFields)+"]")
			},
			expect: []string{"GET " + projectsA + "/apps/web/domains/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				domains, _ := out["domains"].([]any)
				if len(domains) != 1 {
					t.Fatalf("out = %v", out)
				}
				domain, _ := domains[0].(map[string]any)
				if domain["hostname"] != "www.example.com" || domain["cnameTarget"] != "web-proj-a.apps.example.test" || domain["status"] != "pending" {
					t.Errorf("domain = %v", domain)
				}
				for _, hidden := range []string{"plantedSecret", "nested", "projectId", "consecutiveFailures"} {
					if _, ok := domain[hidden]; ok {
						t.Errorf("domain carries %s", hidden)
					}
				}
			},
		},
		{
			name: "add_app_domain", tool: "add_app_domain", args: map[string]any{"project_id": testProjectA, "app_id": "web", "hostname": "www.example.com"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/apps/web/domains/", 201, realJSON(appDomain(), plantedFields))
			},
			expect: []string{"POST " + projectsA + "/apps/web/domains/"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"hostname":"www.example.com"}` {
					t.Errorf("body = %s", calls[0].Body)
				}
				next, _ := out["next"].(string)
				for _, want := range []string{"CNAME", "www.example.com", "web-proj-a.apps.example.test", "verify_app_domain", "dom1"} {
					if !strings.Contains(next, want) {
						t.Errorf("next lacks %q: %s", want, next)
					}
				}
			},
		},
		{
			name: "verify_app_domain", tool: "verify_app_domain", args: map[string]any{"project_id": testProjectA, "app_id": "web", "domain_id": "dom1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/apps/web/domains/dom1/verify", 200, realJSON(appDomain(), plantedFields))
			},
			expect: []string{"POST " + projectsA + "/apps/web/domains/dom1/verify"},
		},
		{
			name: "remove_app_domain", tool: "remove_app_domain", args: map[string]any{"project_id": testProjectA, "app_id": "web", "domain_id": "dom1"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodDelete, projectsA+"/apps/web/domains/dom1", 204, ``) },
			expect: []string{"DELETE " + projectsA + "/apps/web/domains/dom1"},
		},
		{
			name: "connect_app_repo", tool: "connect_app_repo", features: gitOn,
			args: map[string]any{"project_id": testProjectA, "app_id": "web", "repository": "acme/web", "builder": "dockerfile", "build_args": map[string]any{"NODE_ENV": "production"}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, projectsA+"/apps/web/git", 200, realJSON(sitebuilds.AppConnectResult{
					Connection: &sitebuilds.AppConnection{AppID: "web", GitSource: sitebuilds.GitSource{Repository: "acme/web", InstallationID: 77, ConnectedBy: "user-1"},
						Settings: sitebuilds.AppSettings{Branch: "main", Builder: "dockerfile", BuildArgs: map[string]string{"NODE_ENV": "production"}}},
					Build: ptr(appBuild()),
				}, plantedFields))
			},
			expect: []string{"PUT " + projectsA + "/apps/web/git"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				for _, want := range []string{`"repository":"acme/web"`, `"builder":"dockerfile"`, `"NODE_ENV":"production"`} {
					if !strings.Contains(calls[0].Body, want) {
						t.Errorf("body %s lacks %s", calls[0].Body, want)
					}
				}
				connection, _ := out["connection"].(map[string]any)
				build, _ := out["build"].(map[string]any)
				if connection["repository"] != "acme/web" || connection["branch"] != "main" || build["id"] != "ab1" {
					t.Fatalf("out = %v", out)
				}
				if text := realJSON(out, nil); strings.Contains(text, "installationId") || strings.Contains(text, "plantedSecret") || strings.Contains(text, "connectedBy") {
					t.Errorf("out carries a field no view names: %s", text)
				}
			},
		},
		{
			name: "connect_app_repo keeps the current build arguments", tool: "connect_app_repo", features: gitOn,
			args: map[string]any{"project_id": testProjectA, "app_id": "web", "repository": "acme/web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/git", 200, `{"connected":true,"connection":{"repository":"acme/web","settings":{"buildArgs":{"KEPT":"1"}}}}`)
				f.on(http.MethodPut, projectsA+"/apps/web/git", 200, `{"connection":{"repository":"acme/web","settings":{"buildArgs":{"KEPT":"1"}}}}`)
			},
			expect: []string{"GET " + projectsA + "/apps/web/git", "PUT " + projectsA + "/apps/web/git"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if !strings.Contains(calls[1].Body, `"KEPT":"1"`) {
					t.Errorf("build arguments not kept: %s", calls[1].Body)
				}
				if connection, _ := out["connection"].(map[string]any); strings.Contains(realJSON(connection, nil), `"1"`) {
					t.Errorf("connection shows a build argument value: %v", connection)
				}
			},
		},
		{
			name: "disconnect_app_repo", tool: "disconnect_app_repo", features: gitOn, args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodDelete, projectsA+"/apps/web/git", 204, ``) },
			expect: []string{"DELETE " + projectsA + "/apps/web/git"},
		},
		{
			name: "rebuild_app", tool: "rebuild_app", features: gitOn, args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/apps/web/builds", 202, realJSON(appBuild(), plantedFields))
			},
			expect: []string{"POST " + projectsA + "/apps/web/builds"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				build, _ := out["build"].(map[string]any)
				if build["id"] != "ab1" || build["status"] != "QUEUED" {
					t.Fatalf("build = %v", build)
				}
				if text := realJSON(out, nil); strings.Contains(text, "plantedSecret") || strings.Contains(text, "TOKEN") {
					t.Errorf("build carries a field no view names: %s", text)
				}
			},
		},
		{
			name: "list_app_builds", tool: "list_app_builds", features: gitOn, args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/builds", 200, "["+realJSON(appBuild(), plantedFields)+"]")
			},
			expect: []string{"GET " + projectsA + "/apps/web/builds"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				builds, _ := out["builds"].([]any)
				if len(builds) != 1 || strings.Contains(realJSON(out, nil), "plantedSecret") {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "list_app_builds one", tool: "list_app_builds", features: gitOn, args: map[string]any{"project_id": testProjectA, "app_id": "web", "build_id": "ab1"},
			setup: func(f *fakeRoutes) {
				withLog := appBuild()
				withLog.Log = "== docker build =="
				f.on(http.MethodGet, projectsA+"/apps/web/builds/ab1", 200, realJSON(withLog, plantedFields))
			},
			expect: []string{"GET " + projectsA + "/apps/web/builds/ab1"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				build, _ := out["build"].(map[string]any)
				if build["log"] != "== docker build ==" || strings.Contains(realJSON(out, nil), "plantedSecret") {
					t.Errorf("out = %v", out)
				}
			},
		},
	}
}

func ptr[T any](value T) *T { return &value }

// A secret value is write-only: no tool reads one, and the tool never echoes what it was given.
func TestSetAppSecretNeverEchoesTheValue(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodPut, projectsA+"/apps/web/secrets/API_KEY", 200, `{"name":"API_KEY","set":true}`)
	cs := session(t, f, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "set_app_secret", map[string]any{"project_id": testProjectA, "app_id": "web", "name": "API_KEY", "value": "s3cr3t-value"})
	if res.IsError || strings.Contains(resultText(res), "s3cr3t-value") {
		t.Fatalf("result = %s", resultText(res))
	}
	for _, bad := range []map[string]any{{"name": "", "value": "v"}, {"name": "API_KEY", "value": ""}, {"name": "..", "value": "v"}} {
		bad["project_id"], bad["app_id"] = testProjectA, "web"
		if res := callTool(t, cs, "set_app_secret", bad); !res.IsError {
			t.Errorf("accepted %v", bad)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %d, want only the good one", len(f.calls))
	}
}

func TestAppDomainsExplainAnInstallWithoutThem(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodGet, projectsA+"/apps/web/domains/", 404, `404 page not found`)
	res := callTool(t, session(t, f, &recordingAudit{}, writeCaller()), "list_app_domains", map[string]any{"project_id": testProjectA, "app_id": "web"})
	if !res.IsError || !strings.Contains(resultText(res), "custom domains") {
		t.Fatalf("result = %s", resultText(res))
	}
}

func TestConnectAppRepoSendsTheUserToSignInFirst(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodGet, projectsA+"/apps/web/git", 200, `{"connected":false,"connection":null}`)
	f.on(http.MethodPut, projectsA+"/apps/web/git", 403, `{"error":"sign in to GitHub first"}`)
	settings := testSettings
	settings.Features = features.NewStatic(features.AppGitBuilds)
	res := callTool(t, sessionWith(t, f, &recordingAudit{}, writeCaller(), settings), "connect_app_repo",
		map[string]any{"project_id": testProjectA, "app_id": "web", "repository": "acme/web"})
	if !res.IsError || !strings.Contains(resultText(res), "GitHub") || !strings.Contains(resultText(res), "never through MCP") {
		t.Fatalf("result = %s", resultText(res))
	}
}
