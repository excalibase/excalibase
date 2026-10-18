//go:build integration

package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// mcpTenant is one project database registered with the platform's own role
// SQL, and the logins vault holds for it.
type mcpTenant struct {
	super  *sql.DB
	logins map[string]map[string]string
}

func startMCPTenant(t *testing.T) mcpTenant {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("tenant"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432/tcp")
	login := func(user, password string) map[string]string {
		return map[string]string{"host": host, "port": port.Port(), "username": user, "password": password, "database": "tenant"}
	}
	tenant := mcpTenant{logins: map[string]map[string]string{
		"excalibase_app": login("excalibase_app", "mcp-app-pass"),
		"auth_admin":     login("auth_admin", "mcp-auth-pass"),
	}}
	tenant.super = openTenantDB(t, login("postgres", "postgres"))
	if _, err := tenant.super.Exec(service.BuildProjectRoleSQL("mcp-auth-pass", "mcp-app-pass", "mcp-watcher-pass", "tenant", "cdc_watcher_pub") +
		service.BuildSchedulerRoleSQL("mcp-scheduler-pass", "tenant")); err != nil {
		t.Fatalf("register project roles: %v", err)
	}
	authAdmin := openTenantDB(t, tenant.logins["auth_admin"])
	if _, err := authAdmin.Exec(`CREATE TABLE auth.users (id serial PRIMARY KEY, email text);
		INSERT INTO auth.users (email) VALUES ('ada@example.test')`); err != nil {
		t.Fatalf("auth migration: %v", err)
	}
	return tenant
}

func openTenantDB(t *testing.T, creds map[string]string) *sql.DB {
	t.Helper()
	dsn, err := projectdb.DSNFor(creds, projectdb.Overrides{SSLMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// platformRoutes serves the two routes the database tools call with the
// platform's real handlers over the tenant.
func platformRoutes(t *testing.T, tenant mcpTenant) http.Handler {
	t.Helper()
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	instances := fakestore.NewInstances()
	if err := instances.Create(&domain.DatabaseInstance{ProjectID: testProjectA, Status: string(domain.StatusActive), DBType: domain.PostgreSQL}); err != nil {
		t.Fatal(err)
	}
	vault := newMemoryVault()
	for role, creds := range tenant.logins {
		_ = vault.Put("projects/"+testProjectA+"/credentials/"+role, creds)
	}
	opener := projectdb.NewOpener(instances, vault, projectdb.OverridesFromEnv(), projectdb.PoolLimits{})
	t.Cleanup(opener.Close)
	migrations := handler.NewMigrationHandler(service.NewMigrationService(instances, vault, t.TempDir()))

	r := chi.NewRouter()
	r.Route("/api/schema", handler.NewSchemaHandler(opener, 30*time.Second).Routes)
	r.Route("/api/provision/{projectId}/migrations", migrations.Routes)
	r.Get("/api/provision/{projectId}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"databaseType":"POSTGRESQL"}`))
	})
	return r
}

// EXC-623: MCP execute_sql and apply_migration run customer SQL as
// excalibase_app, through the platform's real routes. Neither can write the
// excalibase schema or read the project's auth users (EXC-628).
func TestMCPDatabaseTools_CannotWriteThePlatformSchema(t *testing.T) {
	tenant := startMCPTenant(t)
	cs := session(t, platformRoutes(t, tenant), &recordingAudit{}, writeCaller())

	refusedSQL := []string{
		`CREATE FUNCTION excalibase.planted() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'`,
		`CREATE TABLE excalibase.planted (id int)`,
		`UPDATE excalibase.excalibase_cron_jobs SET args = '{}'`,
		`DROP FUNCTION excalibase.set_realtime_table(text, text, boolean)`,
	}
	for _, statement := range refusedSQL {
		res := callTool(t, cs, "execute_sql", map[string]any{"project_id": testProjectA, "query": statement})
		if !res.IsError {
			t.Errorf("execute_sql allowed: %s", statement)
		} else if text := resultText(res); !strings.Contains(text, "permission denied") && !strings.Contains(text, "must be owner") {
			t.Errorf("execute_sql refused %s for another reason: %s", statement, text)
		}

		res = callTool(t, cs, "apply_migration", map[string]any{"project_id": testProjectA, "name": "plant", "sql": statement})
		migration, _ := structured(t, res)["migration"].(map[string]any)
		if migration["status"] != "FAILED" {
			t.Errorf("apply_migration applied: %s (%v)", statement, migration)
		}
	}

	read := callTool(t, cs, "execute_sql", map[string]any{"project_id": testProjectA, "query": `SELECT email FROM auth.users`})
	if !read.IsError || !strings.Contains(resultText(read), "permission denied") {
		t.Fatalf("execute_sql read of auth.users = %s", resultText(read))
	}
	res := callTool(t, cs, "apply_migration", map[string]any{"project_id": testProjectA, "name": "profiles",
		"sql": `CREATE TABLE public.profiles (user_id int PRIMARY KEY, email text); INSERT INTO public.profiles SELECT id, email FROM auth.users`})
	if migration, _ := structured(t, res)["migration"].(map[string]any); migration["status"] != "FAILED" {
		t.Fatalf("apply_migration copied the auth users: %v", migration)
	}

	var planted int
	if err := tenant.super.QueryRow(`SELECT count(*) FROM pg_proc WHERE proname = 'planted'`).Scan(&planted); err != nil || planted != 0 {
		t.Fatalf("planted functions = %d (%v)", planted, err)
	}
}

// memoryVault is the vault surface the opener and migration service read.
type memoryVault struct{ data map[string]map[string]string }

func newMemoryVault() *memoryVault { return &memoryVault{data: map[string]map[string]string{}} }

func (v *memoryVault) Get(path string) (map[string]string, error) {
	if record, ok := v.data[path]; ok {
		return record, nil
	}
	return nil, errNotInVault
}
func (v *memoryVault) Put(path string, record map[string]string) error {
	v.data[path] = record
	return nil
}
func (v *memoryVault) Delete(path string) error         { delete(v.data, path); return nil }
func (v *memoryVault) DeletePrefix(string) (int, error) { return 0, nil }
func (v *memoryVault) List(string) ([]string, error)    { return nil, nil }
func (v *memoryVault) Sealed() bool                     { return false }
func (v *memoryVault) GetPublicKey() (string, error)    { return "", nil }

var errNotInVault = errors.New("not found")
