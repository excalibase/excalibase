package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

const mysqlProjectReply = `{"projectId":"proj-a","databaseType":"MYSQL","databaseName":"app","status":"ACTIVE"}`

// EXC-619: MySQL column types as the schema API reports them (COLUMN_TYPE).
func TestTypeScriptForMapsMySQLTypes(t *testing.T) {
	tables := []tableColumns{{
		Name: "orders",
		Columns: []schema.ColumnInfo{
			{Name: "id", DataType: "bigint unsigned"},
			{Name: "paid", DataType: "tinyint(1)"},
			{Name: "flags", DataType: "tinyint"},
			{Name: "qty", DataType: "int"},
			{Name: "total", DataType: "decimal(10,2)"},
			{Name: "ratio", DataType: "double", Nullable: true},
			{Name: "status", DataType: "enum('draft','paid')"},
			{Name: "mood", DataType: "enum('it''s','ok')", Nullable: true},
			{Name: "tags", DataType: "set('a','b')"},
			{Name: "meta", DataType: "json", Nullable: true},
			{Name: "placed_at", DataType: "datetime"},
			{Name: "seen_at", DataType: "timestamp(6)"},
			{Name: "day", DataType: "date"},
			{Name: "made_in", DataType: "year"},
			{Name: "note", DataType: "varchar(255)"},
			{Name: "picture", DataType: "blob"},
		},
	}}
	got := typeScriptForEngine("app", tables, engineMySQL)
	for _, want := range []string{
		"  id: number;",
		"  paid: boolean;",
		"  flags: number;",
		"  qty: number;",
		"  total: number;",
		"  ratio: number | null;",
		`  status: "draft" | "paid";`,
		`  mood: "it's" | "ok" | null;`,
		"  tags: string;",
		"  meta: unknown | null;",
		"  placed_at: string;",
		"  seen_at: string;",
		"  day: string;",
		"  made_in: number;",
		"  note: string;",
		"  picture: string;",
		"export interface Database {\n  app: {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Json") {
		t.Errorf("MySQL json is unknown, not the Postgres Json type:\n%s", got)
	}
}

func TestGenerateTypeScriptTypesUsesTheMySQLMapOnAMySQLProject(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA, 200, mysqlProjectReply)
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"orders","schema":"app","type":"BASE TABLE"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/orders/columns", 200, `[{"name":"paid","dataType":"tinyint(1)"},{"name":"meta","dataType":"json"}]`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "generate_typescript_types", map[string]any{"project_id": testProjectA}))
	typescript, _ := out["typescript"].(string)
	for _, want := range []string{"paid: boolean;", "meta: unknown;", "  app: {"} {
		if !strings.Contains(typescript, want) {
			t.Errorf("missing %q in:\n%s", want, typescript)
		}
	}
}

// The runner's notice reaches the caller, and a failure says what stayed.
func TestExecuteSQLReportsMySQLsNonTransactionalDDL(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, schemaA+"/query", 200, `{"command":"EXEC","affectedRows":2,"notice":"MySQL DDL is not transactional"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "execute_sql", map[string]any{"project_id": testProjectA, "query": "CREATE TABLE a (x int); INSERT INTO a VALUES (1), (2)"}))
	if notice, _ := out["notice"].(string); !strings.Contains(notice, "not transactional") {
		t.Fatalf("out = %v", out)
	}

	failing := newFakeRoutes()
	failing.on(http.MethodPost, schemaA+"/query", 200, `{"error":"Table 'a' already exists (MySQL error 1050)","notice":"MySQL DDL is not transactional: statements before the failing one stay applied"}`)
	res := callTool(t, session(t, failing, &recordingAudit{}, writeCaller()), "execute_sql", map[string]any{"project_id": testProjectA, "query": "CREATE TABLE a (x int)"})
	if !res.IsError || !strings.Contains(resultText(res), "already exists") || !strings.Contains(resultText(res), "stay applied") {
		t.Fatalf("res = %s", resultText(res))
	}
}

// A Postgres-only route answers a MySQL project 409 with its reason; the tool
// passes that reason on as its refusal.
func TestPostgresOnlyToolsRefuseAMySQLProjectWithTheReason(t *testing.T) {
	refusal := `{"error":"not available for MySQL projects: migration history is Postgres-only for now"}`
	routes := newFakeRoutes()
	routes.on(http.MethodPost, provisionA+"/migrations/", http.StatusConflict, refusal)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "apply_migration", map[string]any{"project_id": testProjectA, "name": "x", "sql": "CREATE TABLE a (x int)"})
	if !res.IsError || !strings.Contains(resultText(res), "migration history is Postgres-only") {
		t.Fatalf("res = %s", resultText(res))
	}
}

// track_db_function reads a 409 as "already tracked", so a MySQL project is
// refused before the call.
func TestTrackDBFunctionRefusesAMySQLProject(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA, 200, mysqlProjectReply)
	routes.on(http.MethodPost, provisionA+"/tracked-functions/", http.StatusConflict, `{"error":"not available for MySQL projects"}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "track_db_function", map[string]any{"project_id": testProjectA, "function": "app.short_code"})
	if !res.IsError || !strings.Contains(resultText(res), "MySQL functions return scalars") {
		t.Fatalf("res = %s", resultText(res))
	}
	if len(routes.calls) != 1 {
		t.Fatalf("calls = %+v", routes.calls)
	}
}

// A MySQL project's end users live in its own auth database (EXC-626), so
// the probe signs in exactly as it does for a Postgres project.
func TestAuthFlowRunsOnAMySQLProject(t *testing.T) {
	plane := &authPlane{answer: signedIn}
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA, 200, mysqlProjectReply)
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
	settings := testSettings
	settings.DataPlaneURL = server.URL
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
	out := structured(t, callTool(t, cs, "test_auth_flow", flowArgs("login")))
	if out["outcome"] != "signed_in" || out["tokensIssued"] != true {
		t.Fatalf("out = %v", out)
	}
	if len(plane.requests) != 1 || plane.requests[0].URL.Path != loginPath {
		t.Fatalf("auth requests = %d", len(plane.requests))
	}
}

// get_project_info names a MySQL table's root fields by its database, the
// schema the engine reads it under.
func TestProjectInfoNamesMySQLTablesByTheirDatabase(t *testing.T) {
	out := map[string]any{}
	routes := newFakeRoutes()
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"orders","schema":"app","type":"BASE TABLE"}]`)
	c := &call{dispatcher: dispatcher{router: routes, caller: writeCaller(), answered: &[]routeReply{}}, settings: testSettings}
	addGraphQLFields(context.Background(), c, testProjectA, out)
	fields, _ := out["graphqlFields"].(map[string]rootFields)
	if _, ok := fields["app.orders"]; !ok {
		t.Fatalf("fields = %v", out)
	}
}

// EXC-649: a MySQL project's tables are read and named under its database,
// not public (Corner Shop: appProducts, not publicProducts).
const cornerShopProject = `{"projectId":"proj-a","databaseType":"MYSQL","databaseName":"app","status":"ACTIVE"}`

func cornerShopRoutes() *fakeRoutes {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA, 200, cornerShopProject)
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"orders","schema":"app","type":"BASE TABLE"},{"name":"order_items","schema":"app","type":"BASE TABLE"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/orders/columns", 200, `[{"name":"id"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/order_items/columns", 200, `[{"name":"id"},{"name":"order_id"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/orders/indexes", 200, `[]`)
	routes.on(http.MethodGet, schemaA+"/tables/orders/checks", 200, `[]`)
	routes.on(http.MethodGet, schemaA+"/relationships", 200, `[{"constraintName":"items_order","sourceTable":"order_items","sourceColumn":"order_id","targetTable":"orders","targetColumn":"id"}]`)
	routes.on(http.MethodGet, projectsA+"/realtime/tables", 200, `[]`)
	return routes
}

func schemaQueries(calls []recordedCall) []string {
	var queries []string
	for _, call := range calls {
		if strings.HasPrefix(call.Path, schemaA) {
			queries = append(queries, call.Query)
		}
	}
	return queries
}

func TestGetGraphQLSchemaNamesAMySQLProjectsTablesByItsDatabase(t *testing.T) {
	routes := cornerShopRoutes()
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	out := structured(t, callTool(t, cs, "get_graphql_schema", map[string]any{"project_id": testProjectA}))
	if out["schema"] != "app" {
		t.Fatalf("schema = %v", out["schema"])
	}
	fields := map[string]string{}
	for _, raw := range out["tables"].([]any) {
		table := raw.(map[string]any)
		graphql := table["graphql"].(map[string]any)
		fields[table["name"].(string)] = graphql["query"].(string)
		if table["name"] == "order_items" {
			relationships := table["relationships"].([]any)
			if field := relationships[0].(map[string]any)["field"]; field != "appOrder" {
				t.Errorf("order_items relationship = %v", field)
			}
		}
	}
	if fields["orders"] != "appOrders" || fields["order_items"] != "appOrderItems" {
		t.Fatalf("root fields = %v", fields)
	}
	for _, query := range schemaQueries(routes.calls) {
		if query != "schema=app" {
			t.Fatalf("schema routes asked with %q", query)
		}
	}
}

func TestSchemaToolsReadAMySQLProjectsDatabase(t *testing.T) {
	for _, tool := range []string{"list_tables", "describe_table", "generate_typescript_types"} {
		routes := cornerShopRoutes()
		cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
		args := map[string]any{"project_id": testProjectA}
		if tool == "describe_table" {
			args["table"] = "orders"
		}
		out := structured(t, callTool(t, cs, tool, args))
		if tool != "generate_typescript_types" && out["schema"] != "app" {
			t.Errorf("%s schema = %v", tool, out["schema"])
		}
		for _, query := range schemaQueries(routes.calls) {
			if query != "schema=app" {
				t.Errorf("%s asked the schema routes with %q", tool, query)
			}
		}
	}
}

// The permission route's refusal reaches the caller word for word.
func TestSetPermissionReturnsTheEnginesRefusal(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPut, provisionA+"/permissions/tables/app.order_items/roles/anon/select", http.StatusBadRequest,
		`{"error":"permission_invalid: Unknown relationship orders on app.order_items","status":400}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "set_permission", map[string]any{"project_id": testProjectA, "table": "app.order_items", "role": "anon",
		"operation": "select", "permission": map[string]any{"filter": map[string]any{"orders": map[string]any{}}, "columns": "*"}})
	if !res.IsError || !strings.Contains(resultText(res), "permission_invalid: Unknown relationship orders on app.order_items") {
		t.Fatalf("res = %s", resultText(res))
	}
}

// A permission saved under public on a MySQL project names no table: the
// catalogue reads public as the database, so the reply's schema decides.
func TestListPermissionsFlagsAMySQLProjectsPublicTables(t *testing.T) {
	routes := cornerShopRoutes()
	routes.on(http.MethodGet, provisionA+"/permissions/", 200, `{"tables":[{"table":"public.orders","role":"anon"},{"table":"app.orders","role":"anon"}]}`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	out := structured(t, callTool(t, cs, "list_permissions", map[string]any{"project_id": testProjectA}))
	if dropped, _ := out["droppedTables"].([]any); len(dropped) != 1 || dropped[0] != "public.orders" {
		t.Fatalf("droppedTables = %v", out["droppedTables"])
	}
}
