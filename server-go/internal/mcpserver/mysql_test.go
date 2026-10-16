package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

const mysqlProjectReply = `{"projectId":"proj-a","databaseType":"MYSQL","status":"ACTIVE"}`

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

// End users live in excalibase-auth's Postgres store; a MySQL project has none
// yet, so the probe is refused before anything is sent.
func TestAuthFlowRefusesAMySQLProject(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA, 200, mysqlProjectReply)
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), Settings{PublicBaseURL: "https://api.example.test"})
	res := callTool(t, cs, "test_auth_flow", map[string]any{"project_id": testProjectA, "flow": "login", "email": "a@b.test", "password": "pw"})
	if !res.IsError || !strings.Contains(resultText(res), "MySQL") {
		t.Fatalf("res = %s", resultText(res))
	}
	for _, call := range routes.calls {
		if strings.HasSuffix(call.Path, "/info/") {
			t.Fatal("the probe went ahead")
		}
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
