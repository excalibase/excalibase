package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
	"github.com/go-chi/chi/v5"
)

const storedSite = `{"id":"s1","name":"shop","hostname":"shop.example.app","url":"https://shop.example.app","spaFallback":true}`

// gzipMagic is the archive the route cases upload: the route answers from a canned reply.
var gzipMagic = []byte{0x1f, 0x8b, 0x08, 0x00}

func sitesRouteCases() []routeCase {
	archive := base64.StdEncoding.EncodeToString(gzipMagic)
	return []routeCase{
		{
			name: "create_site", tool: "create_site", args: map[string]any{"project_id": testProjectA, "name": "shop", "spa_fallback": false},
			setup:  func(f *fakeRoutes) { f.on(http.MethodPost, projectsA+"/sites/", 201, storedSite) },
			expect: []string{"POST " + projectsA + "/sites/"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"name":"shop","spaFallback":false}` || out["site"] == nil {
					t.Errorf("body %s out %v", calls[0].Body, out)
				}
			},
		},
		{
			name: "list_sites", tool: "list_sites", args: map[string]any{"project_id": testProjectA},
			setup:  func(f *fakeRoutes) { f.on(http.MethodGet, projectsA+"/sites/", 200, "["+storedSite+"]") },
			expect: []string{"GET " + projectsA + "/sites/"},
		},
		{
			name: "deploy_site", tool: "deploy_site", args: map[string]any{"project_id": testProjectA, "site_id": "s1", "archive_base64": archive, "label": "main@abc"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/sites/s1/deploy", 201, `{"id":"d1","status":"SUCCEEDED","live":true}`)
				f.on(http.MethodGet, projectsA+"/sites/s1/", 200, storedSite)
			},
			expect: []string{"POST " + projectsA + "/sites/s1/deploy?label=main%40abc", "GET " + projectsA + "/sites/s1/"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != string(gzipMagic) || calls[0].Header.Get("Content-Type") != "application/gzip" {
					t.Errorf("the archive must go up as the raw body: %q %s", calls[0].Body, calls[0].Header.Get("Content-Type"))
				}
				if out["url"] != "https://shop.example.app" || out["deploy"] == nil {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "list_site_deploys", tool: "list_site_deploys", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/deploys", 200, `[{"id":"d1","status":"SUCCEEDED","live":true}]`)
			},
			expect: []string{"GET " + projectsA + "/sites/s1/deploys"},
		},
		{
			name: "rollback_site", tool: "rollback_site", args: map[string]any{"project_id": testProjectA, "site_id": "s1", "deploy_id": "d1"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodPost, projectsA+"/sites/s1/deploys/d1/rollback", 200, storedSite) },
			expect: []string{"POST " + projectsA + "/sites/s1/deploys/d1/rollback"},
		},
		{
			name: "delete_site", tool: "delete_site", args: map[string]any{"project_id": testProjectA, "site_id": "s1"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodDelete, projectsA+"/sites/s1/", 204, ``) },
			expect: []string{"DELETE " + projectsA + "/sites/s1/"},
		},
		{
			name: "get_ci_snippet for a site", tool: "get_ci_snippet",
			args:   map[string]any{"project_id": testProjectA, "kind": "site", "provider": "gitlab-ci", "site_id": "s1", "context": "apps/web", "output": "build"},
			setup:  func(f *fakeRoutes) { f.on(http.MethodGet, projectsA+"/sites/s1/", 200, storedSite) },
			expect: []string{"GET " + projectsA + "/sites/s1/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				content, _ := out["content"].(string)
				for _, want := range []string{"https://app.example.test/api/projects/proj-a/sites/s1/deploy", `tar czf site.tgz -C "apps/web/build" .`, "npm run build"} {
					if !strings.Contains(content, want) {
						t.Errorf("content lacks %q: %s", want, content)
					}
				}
			},
		},
	}
}

// sitesStack is the production gate stack in front of the real Sites handler and service.
type sitesStack struct {
	router    http.Handler
	publisher *sitestest.Publisher
}

func newSitesStack(t *testing.T, role string, available bool) sitesStack {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Items[testProjectA] = &domain.DatabaseInstance{ProjectID: testProjectA, OrgID: "org-a", Status: "ACTIVE"}
	orgs := fakestore.NewOrgs()
	orgs.AddMember("org-a", testUserID, role)
	var publisher *sitestest.Publisher
	var service *sites.Service
	var err error
	if available {
		publisher = sitestest.NewPublisher()
		service, err = sites.NewService(sitestest.NewMemoryStore(), publisher, sitestest.NewOrigins(),
			sites.Options{BaseDomain: "example.app", SpoolDir: t.TempDir()})
	} else {
		service, err = sites.NewService(sitestest.NewMemoryStore(), nil, sitestest.NewOrigins(), sites.Options{})
	}
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(noTokens{}))
	r.Route("/api/projects/{projectId}/sites", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(instances, orgs))
		handler.NewSiteHandler(service).Routes(r, custommw.RequireProjectRole(domain.OrgRoleDeveloper, instances, orgs))
	})
	return sitesStack{router: r, publisher: publisher}
}

func siteCallArgs(extra map[string]any) map[string]any {
	args := map[string]any{"project_id": testProjectA}
	for key, value := range extra {
		args[key] = value
	}
	return args
}

func encodedDist(t *testing.T) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(sitestest.Dist(t, "<!doctype html><p>shop</p>"))
}

// The tools reach the real routes, so the HTTP path's role gate and the
// sites service's checks decide, not a copy of them here.
func TestSiteToolsDeployThroughTheRealRouteAsADeveloper(t *testing.T) {
	stack := newSitesStack(t, domain.OrgRoleDeveloper, true)
	cs := session(t, stack.router, &recordingAudit{}, writeCaller())
	created := structured(t, callTool(t, cs, "create_site", siteCallArgs(map[string]any{"name": "shop"})))
	site, _ := created["site"].(map[string]any)
	siteID, _ := site["id"].(string)
	if siteID == "" || site["url"] != "https://shop.example.app" {
		t.Fatalf("created = %v", created)
	}
	first := structured(t, callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": siteID, "archive_base64": encodedDist(t), "label": "c1"})))
	if first["url"] != "https://shop.example.app" {
		t.Fatalf("deploy = %v", first)
	}
	firstDeploy, _ := first["deploy"].(map[string]any)
	structured(t, callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": siteID, "archive_base64": encodedDist(t), "label": "c2"})))
	history := structured(t, callTool(t, cs, "list_site_deploys", siteCallArgs(map[string]any{"site_id": siteID})))
	if deploys, _ := history["deploys"].([]any); len(deploys) != 2 {
		t.Fatalf("history = %v", history)
	}
	rolled := structured(t, callTool(t, cs, "rollback_site", siteCallArgs(map[string]any{"site_id": siteID, "deploy_id": firstDeploy["id"]})))
	if back, _ := rolled["site"].(map[string]any); back["liveDeployId"] != firstDeploy["id"] {
		t.Fatalf("rollback = %v", rolled)
	}
	structured(t, callTool(t, cs, "delete_site", siteCallArgs(map[string]any{"site_id": siteID})))
	if listed := structured(t, callTool(t, cs, "list_sites", siteCallArgs(nil))); len(listed["sites"].([]any)) != 0 {
		t.Fatalf("after delete = %v", listed)
	}
	if len(stack.publisher.Deleted) != 1 {
		t.Fatalf("the site's Worker was not removed: %v", stack.publisher.Deleted)
	}
}

func TestSiteToolsRefuseAViewerEveryChangeButLetThemRead(t *testing.T) {
	stack := newSitesStack(t, domain.OrgRoleViewer, true)
	cs := session(t, stack.router, &recordingAudit{}, writeCaller())
	if out := structured(t, callTool(t, cs, "list_sites", siteCallArgs(nil))); out["sites"] == nil {
		t.Fatalf("a viewer reads the sites: %v", out)
	}
	for name, args := range map[string]map[string]any{
		"create_site":   {"name": "shop"},
		"deploy_site":   {"site_id": "s1", "archive_base64": encodedDist(t)},
		"rollback_site": {"site_id": "s1", "deploy_id": "d1"},
		"delete_site":   {"site_id": "s1"},
	} {
		res := callTool(t, cs, name, siteCallArgs(args))
		if !res.IsError || !strings.Contains(resultText(res), "403") {
			t.Errorf("%s as a viewer = %s", name, resultText(res))
		}
	}
	if stack.publisher.Sessions != 0 {
		t.Fatalf("a viewer's deploy reached the CDN")
	}
}

func TestDeploySiteArchiveIsCheckedByTheSitesService(t *testing.T) {
	stack := newSitesStack(t, domain.OrgRoleDeveloper, true)
	cs := session(t, stack.router, &recordingAudit{}, writeCaller())
	created := structured(t, callTool(t, cs, "create_site", siteCallArgs(map[string]any{"name": "shop"})))
	siteID := created["site"].(map[string]any)["id"]
	climbing := sitestest.Gzip(t, sitestest.Tar(t, sitestest.File("../escape.html", "x"), sitestest.File("index.html", "x")))
	res := callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": siteID, "archive_base64": base64.StdEncoding.EncodeToString(climbing)}))
	if !res.IsError || !strings.Contains(resultText(res), "400") {
		t.Fatalf("a climbing archive = %s", resultText(res))
	}
	if stack.publisher.Sessions != 0 {
		t.Fatalf("a refused archive reached the CDN")
	}
}

func TestSiteToolsWithoutCloudflarePointToContainers(t *testing.T) {
	stack := newSitesStack(t, domain.OrgRoleDeveloper, false)
	cs := session(t, stack.router, &recordingAudit{}, writeCaller())
	for name, args := range map[string]map[string]any{
		"create_site":       {"name": "shop"},
		"list_sites":        {},
		"deploy_site":       {"site_id": "s1", "archive_base64": encodedDist(t)},
		"list_site_deploys": {"site_id": "s1"},
		"rollback_site":     {"site_id": "s1", "deploy_id": "d1"},
		"delete_site":       {"site_id": "s1"},
		"get_ci_snippet":    {"kind": "site", "provider": "curl", "site_id": "s1"},
	} {
		res := callTool(t, cs, name, siteCallArgs(args))
		text := resultText(res)
		if !res.IsError || !strings.Contains(text, "not available on this install") || !strings.Contains(text, "create_app") || !strings.Contains(text, "get_dockerfile_template") {
			t.Errorf("%s = %s", name, text)
		}
	}
}

func TestDeploySiteRefusesABadArchiveBeforeAnyCall(t *testing.T) {
	small := base64.StdEncoding.EncodeToString(gzipMagic)
	oversized := base64.StdEncoding.EncodeToString(make([]byte, maxSiteArchiveBytes+1))
	cases := []struct {
		name, siteID, archive string
	}{
		{"nothing", "s1", ""},
		{"not base64", "s1", "%%% not base64 %%%"},
		{"too large", "s1", oversized},
		{"a site id that climbs", "..", small},
	}
	for _, tc := range cases {
		routes := newFakeRoutes()
		cs := session(t, routes, &recordingAudit{}, writeCaller())
		res := callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": tc.siteID, "archive_base64": tc.archive}))
		if !res.IsError || len(routes.calls) != 0 {
			t.Errorf("%s: error %v calls %d: %s", tc.name, res.IsError, len(routes.calls), resultText(res))
		}
		if tc.name == "too large" && !strings.Contains(resultText(res), "get_ci_snippet") {
			t.Errorf("a build too large for MCP must point to the CI upload: %s", resultText(res))
		}
	}
}

// An archive copied out of a terminal keeps its line breaks; they are not part of the data.
func TestDeploySiteAcceptsWrappedBase64(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/sites/s1/deploy", 201, `{"id":"d1","status":"SUCCEEDED","live":true}`)
	routes.on(http.MethodGet, projectsA+"/sites/s1/", 200, storedSite)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	encoded := base64.StdEncoding.EncodeToString(gzipMagic)
	structured(t, callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": "s1", "archive_base64": encoded[:4] + "\n" + encoded[4:] + "\n"})))
	if routes.calls[0].Body != string(gzipMagic) {
		t.Fatalf("body = %q", routes.calls[0].Body)
	}
}

func TestDeploySiteKeepsTheDeployWhenTheSiteCannotBeReadAfter(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/sites/s1/deploy", 201, `{"id":"d1","status":"SUCCEEDED","live":true}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": "s1", "archive_base64": base64.StdEncoding.EncodeToString(gzipMagic)})))
	if out["deploy"] == nil {
		t.Fatalf("out = %v", out)
	}
}

func TestSiteCISnippetNeedsNoImage(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_ci_snippet", siteCallArgs(map[string]any{"kind": "site", "provider": "github-actions"})))
	if content, _ := out["content"].(string); !strings.Contains(content, "/sites/<site id>/deploy") {
		t.Fatalf("out = %v", out)
	}
	res := callTool(t, cs, "get_ci_snippet", siteCallArgs(map[string]any{"kind": "worker", "provider": "curl"}))
	if !res.IsError || !strings.Contains(resultText(res), "app or site") {
		t.Fatalf("an unknown kind = %s", resultText(res))
	}
}

// The largest archive deploy_site takes must fit the /mcp message cap over real HTTP.
func TestTheLargestSiteArchiveFitsOneMCPMessage(t *testing.T) {
	received := make(chan int, 1)
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(knownTokens{}))
	r.With(auth.RequireAuth).Post("/api/projects/{projectId}/sites/{siteId}/deploy", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- len(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"d1","status":"SUCCEEDED","live":true}`))
	})
	r.Handle("/mcp", NewHandler(r, &recordingAudit{}, testSettings))
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	cs, err := connectOverHTTP(t, server.URL+"/mcp", rawWriteToken)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	archive := make([]byte, maxSiteArchiveBytes)
	if _, err := rand.Read(archive); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": "s1", "archive_base64": base64.StdEncoding.EncodeToString(archive)}))
	if res.IsError {
		t.Fatalf("deploy = %s", resultText(res))
	}
	if got := <-received; got != maxSiteArchiveBytes {
		t.Fatalf("the route received %d bytes, want %d", got, maxSiteArchiveBytes)
	}
}

// EXC-620: a site's pipeline holds the site's deploy token, and the tools that
// point there say so; MCP's own deploys keep the caller's personal token.
func TestSiteCISnippetUsesTheSitesDeployToken(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_ci_snippet", siteCallArgs(map[string]any{"kind": "site", "provider": "curl"})))
	content, _ := out["content"].(string)
	secrets := fmt.Sprint(out["secrets"])
	if !strings.Contains(content, "$EXCALIBASE_DEPLOY_TOKEN") || !strings.Contains(secrets, "EXCALIBASE_DEPLOY_TOKEN") || !strings.Contains(secrets, "deploy token") {
		t.Fatalf("content %q secrets %s", content, secrets)
	}
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, listedTool := range listed.Tools {
		if listedTool.Name == "get_ci_snippet" && !strings.Contains(listedTool.Description, "EXCALIBASE_DEPLOY_TOKEN") {
			t.Errorf("get_ci_snippet does not name the site's deploy token: %s", listedTool.Description)
		}
	}
	oversized := base64.StdEncoding.EncodeToString(make([]byte, maxSiteArchiveBytes+1))
	res := callTool(t, cs, "deploy_site", siteCallArgs(map[string]any{"site_id": "s1", "archive_base64": oversized}))
	if !strings.Contains(resultText(res), "deploy token") {
		t.Errorf("a build too large for MCP must point to the site's deploy token: %s", resultText(res))
	}
}
