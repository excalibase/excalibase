package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordedCall is one in-process request a tool made.
type recordedCall struct {
	Method string
	Path   string
	Query  string
	Body   string
	Header http.Header
	Token  *domain.AccessToken
}

// fakeRoutes stands in for the router: it records every call and answers
// from canned replies, 404 for anything it was not told about.
type fakeRoutes struct {
	mu      sync.Mutex
	calls   []recordedCall
	replies map[string]cannedReply
}

type cannedReply struct {
	status int
	body   string
}

func newFakeRoutes() *fakeRoutes {
	return &fakeRoutes{replies: map[string]cannedReply{}}
}

func (f *fakeRoutes) on(method, path string, status int, body string) {
	f.replies[method+" "+path] = cannedReply{status: status, body: body}
}

func (f *fakeRoutes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body),
		Header: r.Header.Clone(), Token: auth.GetToken(r.Context()),
	})
	f.mu.Unlock()
	reply, ok := f.replies[r.Method+" "+r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(reply.status)
	_, _ = w.Write([]byte(reply.body))
}

// recordingAudit keeps every entry the endpoint writes.
type recordingAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *recordingAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *entry)
	return nil
}

var testSettings = Settings{PublicBaseURL: "https://api.example.test", StudioURL: "https://app.example.test"}

func writeCaller() Caller {
	return Caller{
		User:  &domain.User{ID: testUserID, Active: true},
		Token: &domain.AccessToken{TokenHash: "hash-1", Name: "laptop", UserID: testUserID, Scopes: auth.ScopeWrite},
	}
}

func readOnlyCaller() Caller {
	c := writeCaller()
	c.ReadOnly = true
	c.Token = &domain.AccessToken{TokenHash: "hash-1", Name: "laptop", UserID: testUserID, Scopes: auth.ScopeRead}
	return c
}

// session connects an in-memory MCP client to the server a caller gets.
func session(t *testing.T, routes http.Handler, audit AuditLogger, caller Caller) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := newServer(&env{router: routes, audit: audit, settings: testSettings}, caller)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool failed: %s", resultText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content is not an object: %s", raw)
	}
	return out
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// routeCase is one tool call and the requests it must make.
type routeCase struct {
	name   string
	tool   string
	args   map[string]any
	setup  func(f *fakeRoutes)
	expect []string
	check  func(t *testing.T, calls []recordedCall, out map[string]any)
}

const (
	schemaA    = "/api/schema/" + testProjectA
	provisionA = "/api/provision/" + testProjectA
	projectsA  = "/api/projects/" + testProjectA
)

func routeCases() []routeCase {
	return []routeCase{
		{
			name: "list_projects", tool: "list_projects", args: map[string]any{},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, "/api/provision/", 200, `[{"projectId":"proj-a","projectName":"A","status":"ACTIVE","orgId":"o"}]`)
			},
			expect: []string{"GET /api/provision/"},
		},
		{
			name: "get_project_info", tool: "get_project_info", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","projectName":"A","orgSlug":"acme","orgId":"o"}`)
				f.on(http.MethodGet, projectsA+"/sdk-keys/", 200, `{"keys":[{"id":1,"keyPrefix":"esk_pub_live_ab","keyType":"publishable","name":"web"},{"id":2,"keyPrefix":"esk_sec_live_cd","keyType":"secret","name":"server"}]}`)
			},
			expect: []string{"GET " + projectsA + "/info/", "GET " + projectsA + "/sdk-keys/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				endpoints, _ := out["endpoints"].(map[string]any)
				if endpoints["graphql"] != "https://api.example.test/proj-a/graphql" || endpoints["rest"] != "https://api.example.test/proj-a/api/v1" {
					t.Errorf("endpoints = %v", endpoints)
				}
				keys, _ := out["publishableKeys"].([]any)
				if len(keys) != 1 {
					t.Errorf("only publishable keys may be listed, got %v", keys)
				}
				setup, _ := out["sdkSetup"].(map[string]any)
				if !strings.Contains(setup["snippet"].(string), `projectId: "proj-a"`) {
					t.Errorf("snippet = %v", setup["snippet"])
				}
			},
		},
		{
			name: "create_publishable_key", tool: "create_publishable_key", args: map[string]any{"project_id": testProjectA, "name": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/sdk-keys/", 201, `{"id":3,"keyPrefix":"esk_pub_live_x","keyType":"publishable","name":"web","plaintext":"esk_pub_live_xyz"}`)
			},
			expect: []string{"POST " + projectsA + "/sdk-keys/"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[0].Body, `"keyType":"publishable"`) {
					t.Errorf("only a publishable key may be created, body %s", calls[0].Body)
				}
			},
		},
		{
			name: "list_tables", tool: "list_tables", args: map[string]any{"project_id": testProjectA, "schema": "app"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"orders","schema":"app","type":"table"}]`)
			},
			expect: []string{"GET " + schemaA + "/tables?schema=app"},
		},
		{
			name: "describe_table", tool: "describe_table", args: map[string]any{"project_id": testProjectA, "table": "orders"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, schemaA+"/tables/orders/columns", 200, `[{"name":"id","dataType":"integer","primaryKey":true}]`)
				f.on(http.MethodGet, schemaA+"/tables/orders/indexes", 200, `[]`)
				f.on(http.MethodGet, schemaA+"/relationships", 200, `[{"sourceTable":"orders","sourceColumn":"customer_id","targetTable":"customers","targetColumn":"id"},{"sourceTable":"items","sourceColumn":"order_id","targetTable":"orders","targetColumn":"id"},{"sourceTable":"a","targetTable":"b"}]`)
			},
			expect: []string{"GET " + schemaA + "/tables/orders/columns?schema=public", "GET " + schemaA + "/tables/orders/indexes?schema=public", "GET " + schemaA + "/relationships?schema=public"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if fks, _ := out["foreignKeys"].([]any); len(fks) != 1 {
					t.Errorf("foreignKeys = %v", out["foreignKeys"])
				}
				if refs, _ := out["referencedBy"].([]any); len(refs) != 1 {
					t.Errorf("referencedBy = %v", out["referencedBy"])
				}
			},
		},
		{
			name: "get_graphql_schema", tool: "get_graphql_schema", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"orders","schema":"public","type":"table"}]`)
				f.on(http.MethodGet, schemaA+"/tables/orders/columns", 200, `[{"name":"id","dataType":"integer","primaryKey":true}]`)
				f.on(http.MethodGet, schemaA+"/relationships", 200, `[]`)
			},
			expect: []string{"GET " + schemaA + "/tables?schema=public", "GET " + schemaA + "/relationships?schema=public", "GET " + schemaA + "/tables/orders/columns?schema=public"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["endpoint"] != "https://api.example.test/proj-a/graphql" {
					t.Errorf("endpoint = %v", out["endpoint"])
				}
			},
		},
		{
			name: "execute_sql read-write", tool: "execute_sql", args: map[string]any{"project_id": testProjectA, "query": "select 1"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, schemaA+"/query", 200, `{"columns":[{"name":"x","dataType":"INT4"}],"rows":[[1]]}`)
			},
			expect: []string{"POST " + schemaA + "/query"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if !strings.Contains(calls[0].Body, `"query":"select 1"`) {
					t.Errorf("body = %s", calls[0].Body)
				}
				if rows, _ := out["rows"].([]any); len(rows) != 1 {
					t.Errorf("rows = %v", out["rows"])
				}
			},
		},
		{
			name: "list_migrations", tool: "list_migrations", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/migrations/", 200, `[{"id":"m1","sql":"create table t()","status":"APPLIED"}]`)
			},
			expect: []string{"GET " + provisionA + "/migrations/"},
		},
		{
			name: "apply_migration", tool: "apply_migration", args: map[string]any{"project_id": testProjectA, "name": "add_orders", "sql": "create table orders(id int)"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, provisionA+"/migrations/", 200, `{"id":"m2","status":"APPLIED","sql":"create table orders(id int)"}`)
			},
			expect: []string{"POST " + provisionA + "/migrations/"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[0].Body, `"name":"add_orders"`) || !strings.Contains(calls[0].Body, `"sql":"create table orders(id int)"`) {
					t.Errorf("body = %s", calls[0].Body)
				}
			},
		},
		{
			name: "generate_typescript_types", tool: "generate_typescript_types", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"order_items","schema":"public","type":"table"}]`)
				f.on(http.MethodGet, schemaA+"/tables/order_items/columns", 200, `[{"name":"id","dataType":"bigint","nullable":false},{"name":"note","dataType":"text","nullable":true}]`)
			},
			expect: []string{"GET " + schemaA + "/tables?schema=public", "GET " + schemaA + "/tables/order_items/columns?schema=public"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				ts, _ := out["typescript"].(string)
				if !strings.Contains(ts, "export interface OrderItems") || !strings.Contains(ts, "note: string | null;") {
					t.Errorf("typescript = %s", ts)
				}
			},
		},
		{
			name: "list_permissions", tool: "list_permissions", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/permissions/", 200, `{"tables":[]}`)
			},
			expect: []string{"GET " + provisionA + "/permissions/"},
		},
		{
			name: "set_permission", tool: "set_permission",
			args: map[string]any{"project_id": testProjectA, "table": "public.orders", "role": "anon", "operation": "select", "permission": map[string]any{"filter": map[string]any{}, "columns": []string{"id"}}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, provisionA+"/permissions/tables/public.orders/roles/anon/select", 200, `{"filter":{},"columns":["id"]}`)
			},
			expect: []string{"PUT " + provisionA + "/permissions/tables/public.orders/roles/anon/select"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[0].Body, `"columns":["id"]`) {
					t.Errorf("body = %s", calls[0].Body)
				}
			},
		},
		{
			name: "set_permission remove", tool: "set_permission",
			args: map[string]any{"project_id": testProjectA, "table": "public.orders", "role": "anon", "operation": "select", "remove": true},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodDelete, provisionA+"/permissions/tables/public.orders/roles/anon/select", 204, ``)
			},
			expect: []string{"DELETE " + provisionA + "/permissions/tables/public.orders/roles/anon/select"},
		},
		{
			name: "list_functions", tool: "list_functions", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/functions/", 200, `[{"id":"hello","name":"hello","files":[{"path":"index.ts","content":"x"}],"version":2,"active":true}]`)
			},
			expect: []string{"GET " + projectsA + "/functions/"},
		},
		{
			name: "deploy_function", tool: "deploy_function",
			args: map[string]any{"project_id": testProjectA, "id": "hello", "files": []map[string]any{{"path": "index.ts", "content": "export default () => new Response('hi')"}}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/functions/", 201, `{"id":"hello","version":1}`)
			},
			expect: []string{"POST " + projectsA + "/functions/"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if !strings.Contains(calls[0].Body, `"id":"hello"`) || !strings.Contains(calls[0].Body, `"path":"index.ts"`) {
					t.Errorf("body = %s", calls[0].Body)
				}
			},
		},
		{
			name: "set_function_secret", tool: "set_function_secret", args: map[string]any{"project_id": testProjectA, "key": "STRIPE_KEY", "value": "sk_test"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/functions/secrets", 200, `{"status":"set","key":"STRIPE_KEY"}`)
			},
			expect: []string{"POST " + projectsA + "/functions/secrets"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				raw, _ := json.Marshal(out)
				if strings.Contains(string(raw), "sk_test") {
					t.Errorf("the secret value must never be echoed: %s", raw)
				}
			},
		},
		{
			name: "list_apps", tool: "list_apps", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/", 200, `[{"id":"web","name":"web","image":"ghcr.io/a/web:1","status":"RUNNING","version":3,"env":[{"name":"X","value":"y"}]}]`)
			},
			expect: []string{"GET " + projectsA + "/apps/"},
		},
		{
			name: "deploy_app with a new image", tool: "deploy_app",
			args: map[string]any{"project_id": testProjectA, "app_id": "web", "image": "ghcr.io/a/web@sha256:" + strings.Repeat("a", 64)},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","image":"ghcr.io/a/web:1","version":3}`)
				f.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","image":"ghcr.io/a/web@sha256:aa","version":4}`)
				f.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"PENDING"}`)
			},
			expect: []string{"GET " + projectsA + "/apps/web/", "PATCH " + projectsA + "/apps/web/", "POST " + projectsA + "/apps/web/deploy"},
			check: func(t *testing.T, calls []recordedCall, _ map[string]any) {
				if calls[1].Header.Get("If-Match") != "3" {
					t.Errorf("the image change must name the version it read, If-Match=%q", calls[1].Header.Get("If-Match"))
				}
				if !strings.Contains(calls[1].Body, `"image":"ghcr.io/a/web@sha256:`) {
					t.Errorf("patch body = %s", calls[1].Body)
				}
			},
		},
		{
			name: "deploy_app as it stands", tool: "deploy_app", args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"PENDING"}`)
			},
			expect: []string{"POST " + projectsA + "/apps/web/deploy"},
		},
		{
			name: "get_deploy_status", tool: "get_deploy_status", args: map[string]any{"project_id": testProjectA, "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","status":"RUNNING","version":4}`)
				f.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, `[{"id":"d1","status":"SUCCEEDED"}]`)
			},
			expect: []string{"GET " + projectsA + "/apps/web/", "GET " + projectsA + "/apps/web/deploys?limit=5"},
		},
		{
			name: "get_logs database", tool: "get_logs", args: map[string]any{"project_id": testProjectA, "source": "database", "lines": 50},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/logs", 200, `{"logs":"line"}`)
			},
			expect: []string{"GET " + provisionA + "/logs?lines=50"},
		},
		{
			name: "get_logs app", tool: "get_logs", args: map[string]any{"project_id": testProjectA, "source": "app", "app_id": "web"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/logs", 200, `{"lines":[]}`)
			},
			expect: []string{"GET " + projectsA + "/apps/web/logs?tail=200"},
		},
		{
			name: "get_logs function", tool: "get_logs", args: map[string]any{"project_id": testProjectA, "source": "function", "function_id": "hello"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/functions/hello/logs", 200, `{"logs":[]}`)
			},
			expect: []string{"GET " + projectsA + "/functions/hello/logs"},
		},
		{
			name: "get_dockerfile_template", tool: "get_dockerfile_template", args: map[string]any{"stack": "go"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if !strings.Contains(out["content"].(string), "FROM golang:") {
					t.Errorf("content = %v", out["content"])
				}
			},
		},
		{
			name: "get_ci_snippet", tool: "get_ci_snippet", args: map[string]any{"provider": "github-actions", "project_id": testProjectA, "app_id": "web"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				content := out["content"].(string)
				if !strings.Contains(content, "https://app.example.test/api/projects/proj-a/apps/web") || !strings.Contains(content, "/deploy") {
					t.Errorf("content = %s", content)
				}
			},
		},
	}
}

func callKey(c recordedCall) string {
	key := c.Method + " " + c.Path
	if c.Query != "" {
		key += "?" + c.Query
	}
	return key
}

func TestEveryToolGoesThroughItsRoute(t *testing.T) {
	for _, tc := range routeCases() {
		t.Run(tc.name, func(t *testing.T) {
			routes := newFakeRoutes()
			if tc.setup != nil {
				tc.setup(routes)
			}
			cs := session(t, routes, &recordingAudit{}, writeCaller())
			out := structured(t, callTool(t, cs, tc.tool, tc.args))
			got := make([]string, 0, len(routes.calls))
			for _, call := range routes.calls {
				got = append(got, callKey(call))
				if call.Token == nil || call.Token.TokenHash != "hash-1" {
					t.Errorf("%s ran without the caller's credential", callKey(call))
				}
			}
			if strings.Join(got, "\n") != strings.Join(tc.expect, "\n") {
				t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.expect, "\n"))
			}
			if tc.check != nil {
				tc.check(t, routes.calls, out)
			}
		})
	}
}

func TestReadOnlyConnectionOffersNoWriteTool(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, readOnlyCaller())
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	offered := map[string]bool{}
	for _, tool := range listed.Tools {
		offered[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is offered on a read-only connection without being read-only", tool.Name)
		}
	}
	for _, name := range []string{"apply_migration", "set_permission", "deploy_function", "set_function_secret", "deploy_app", "create_publishable_key"} {
		if offered[name] {
			t.Errorf("%s must not be offered on a read-only connection", name)
		}
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !res.IsError {
			t.Errorf("%s ran on a read-only connection", name)
		}
	}
}

func TestReadOnlyExecuteSQLUsesTheReadOnlyRoute(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, schemaA+"/query", 200, `{"columns":[{"name":"n","dataType":"INT8"}],"rows":[[3]]}`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	out := structured(t, callTool(t, cs, "execute_sql", map[string]any{"project_id": testProjectA, "query": "select count(*) from t"}))
	if len(routes.calls) != 1 || routes.calls[0].Method != http.MethodGet || !strings.Contains(routes.calls[0].Query, "sql=select") {
		t.Fatalf("calls = %+v", routes.calls)
	}
	if routes.calls[0].Token.Scopes != auth.ScopeRead {
		t.Errorf("read-only SQL must run with the read-only credential")
	}
	if rows, _ := out["rows"].([]any); len(rows) != 1 {
		t.Errorf("rows = %v", out["rows"])
	}
}

func TestSQLErrorIsAToolError(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, schemaA+"/query", 200, `{"error":"relation \"nope\" does not exist"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "execute_sql", map[string]any{"project_id": testProjectA, "query": "select * from nope"})
	if !res.IsError || !strings.Contains(resultText(res), "does not exist") {
		t.Fatalf("res = %+v", res)
	}
}

func TestRouteRefusalIsAToolError(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, provisionA+"/migrations/", 403, `{"error":"insufficient project role"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "apply_migration", map[string]any{"project_id": testProjectA, "name": "x", "sql": "select 1"})
	if !res.IsError || !strings.Contains(resultText(res), "insufficient project role") {
		t.Fatalf("res = %+v", res)
	}
}

func TestBoundConnectionRefusesAnotherProjectBeforeAnyCall(t *testing.T) {
	routes := newFakeRoutes()
	caller := writeCaller()
	caller.Project = testProjectA
	caller.Token.ProjectID = testProjectA
	cs := session(t, routes, &recordingAudit{}, caller)
	res := callTool(t, cs, "list_tables", map[string]any{"project_id": testProjectB})
	if !res.IsError {
		t.Fatalf("a bound connection reached another project")
	}
	if len(routes.calls) != 0 {
		t.Fatalf("calls = %+v", routes.calls)
	}
}

func TestBoundConnectionListsOnlyItsProject(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, "/api/provision/", 200, `[{"projectId":"proj-a"},{"projectId":"proj-b"}]`)
	caller := writeCaller()
	caller.Project = testProjectA
	cs := session(t, routes, &recordingAudit{}, caller)
	out := structured(t, callTool(t, cs, "list_projects", map[string]any{}))
	if projects, _ := out["projects"].([]any); len(projects) != 1 {
		t.Fatalf("projects = %v", out["projects"])
	}
}

// TestACallIsFiledUnderAProjectOnlyWhenTheProjectAnswered pins that naming a
// project is not enough to land in its activity: a non-member's call (the
// access gate answers 404) or a tool that reaches no project route is kept
// without the project, so an outsider cannot write into someone's feed.
func TestACallIsFiledUnderAProjectOnlyWhenTheProjectAnswered(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, schemaA+"/tables", 404, `{"error":"project not found"}`)
	routes.on(http.MethodGet, "/api/schema/"+testProjectB+"/tables", 401, `{"error":"unauthenticated"}`)
	audit := &recordingAudit{}
	cs := session(t, routes, audit, writeCaller())
	callTool(t, cs, "list_tables", map[string]any{"project_id": testProjectA})
	callTool(t, cs, "list_tables", map[string]any{"project_id": testProjectB})
	callTool(t, cs, "get_ci_snippet", map[string]any{"project_id": testProjectA, "provider": "github-actions"})

	if len(audit.entries) != 3 {
		t.Fatalf("entries = %+v", audit.entries)
	}
	for _, entry := range audit.entries {
		if entry.ProjectID != "" {
			t.Errorf("%s was filed under %q without the project answering", entry.ResourceID, entry.ProjectID)
		}
	}
}

func TestEveryToolCallIsAudited(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[]`)
	routes.on(http.MethodPost, provisionA+"/migrations/", 403, `{"error":"insufficient project role"}`)
	audit := &recordingAudit{}
	cs := session(t, routes, audit, writeCaller())
	callTool(t, cs, "list_tables", map[string]any{"project_id": testProjectA})
	callTool(t, cs, "apply_migration", map[string]any{"project_id": testProjectA, "name": "x", "sql": "select 1"})

	if len(audit.entries) != 2 {
		t.Fatalf("entries = %+v", audit.entries)
	}
	ok, refused := audit.entries[0], audit.entries[1]
	for _, entry := range audit.entries {
		if entry.Via != "mcp" || entry.ProjectID != testProjectA || entry.TokenHash != "hash-1" || entry.UserID != testUserID {
			t.Errorf("entry = %+v", entry)
		}
	}
	if ok.ResourceID != "list_tables" || !strings.Contains(ok.Details, `"status":"ok"`) || !strings.Contains(ok.Details, `"tokenName":"laptop"`) {
		t.Errorf("ok entry = %+v", ok)
	}
	if refused.ResourceID != "apply_migration" || !strings.Contains(refused.Details, `"status":"error"`) || !strings.Contains(refused.Details, `"httpStatus":403`) {
		t.Errorf("refused entry = %+v", refused)
	}
	if strings.Contains(refused.Details, "select 1") {
		t.Errorf("the audit must not keep the call's arguments: %s", refused.Details)
	}
}
