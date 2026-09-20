//go:build integration

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const wiringProjectID = "proj_wiring"

// startProjectPG runs a Postgres that stands in for one tenant's database
// and returns a pool plus the credentials an operator would have filed in
// vault for it.
func startProjectPG(t *testing.T) (*sql.DB, map[string]string) {
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

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	creds := map[string]string{
		"host": host, "port": port.Port(),
		"username": "postgres", "password": "postgres", "database": "tenant",
	}
	db, err := sql.Open("postgres", projectdb.DSN(creds, projectdb.Overrides{SSLMode: "disable"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, creds
}

// wiringOpener builds the production project-database opener over one
// project whose credentials are in vault.
func wiringOpener(t *testing.T, creds map[string]string, status string) *projectdb.Opener {
	t.Helper()
	instances := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		wiringProjectID: {ProjectID: wiringProjectID, OrgID: "default", Status: status},
	}}
	vault := &e2eFakeVault{data: map[string]map[string]string{
		fmt.Sprintf("projects/%s/credentials/excalibase_app", wiringProjectID): creds,
	}}
	opener := projectdb.NewOpener(instances, vault, projectdb.Overrides{SSLMode: "disable"}, projectdb.PoolLimits{})
	t.Cleanup(opener.Close)
	return opener
}

// deployHandler wires a handler the way the production builder does: the
// project-database resolver comes from the opener, auto-migrate is on, and
// the runtime is the supplied fake.
func deployHandler(t *testing.T, opener *projectdb.Opener, runtimeURL, status string) *FunctionHandler {
	t.Helper()
	instances := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		wiringProjectID: {ProjectID: wiringProjectID, OrgID: "default", Status: status},
	}}
	store := edgefn.NewFunctionStore(t.TempDir())
	client := edgefn.NewRuntimeClient(runtimeURL, "runtime-shared")
	h := NewFunctionHandler(store, nil, client, instances, nil, "")
	h.SetProjectDBFn(opener.Open)
	h.SetAutoMigrate(true)
	return h
}

// deployModule records a function under moduleName so the sweep's registry
// check recognises rows naming it — the platform only runs what it deployed.
func deployModule(t *testing.T, h *FunctionHandler, projectID, moduleName string) {
	t.Helper()
	if err := h.store.Save(&edgefn.Function{
		ID: moduleName, ProjectID: projectID, Name: moduleName,
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}); err != nil {
		t.Fatalf("save function: %v", err)
	}
}

// With the resolver wired, a deploy that declares a table creates it and
// the cron sync reaches the project's reserved schema. Both were skipped in
// silence while the production builder left the resolver nil.
func TestDeploy_AppliesSchemaAndSyncsCronsThroughTheOpener(t *testing.T) {
	db, creds := startProjectPG(t)
	opener := wiringOpener(t, creds, "ACTIVE")

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	h := deployHandler(t, opener, runtime.URL, "ACTIVE")

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) { r.Post("/", h.Create) })

	userSchema := `
import { defineSchema, defineTable, v } from "./_lib.ts";
export default defineSchema({
  notes: defineTable(v.object({ title: v.string(), body: v.string() })),
});
`
	body := map[string]any{
		"id": "wiring-app", "name": "Wiring App",
		"files": []map[string]string{
			{"path": "_lib.ts", "content": schemaLibBundle},
			{"path": testIndexTS, "content": userSchema},
		},
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/api/projects/"+wiringProjectID+"/functions/", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: got %d, body %s", w.Code, w.Body.String())
	}

	var tableExists bool
	if err := db.QueryRow(
		`SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname = 'nosql' AND tablename = 'notes')`,
	).Scan(&tableExists); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if !tableExists {
		t.Error("the declared table was not created in the project database")
	}

	var cronTable bool
	if err := db.QueryRow(
		`SELECT to_regclass('excalibase.excalibase_cron_jobs') IS NOT NULL`,
	).Scan(&cronTable); err != nil {
		t.Fatalf("query cron table: %v", err)
	}
	if !cronTable {
		t.Error("the cron sync did not reach the project database")
	}
}

// End to end over the real seams: a task the runtime deferred into the
// tenant database is claimed by the sweep and dispatched to the project's
// runtime through the production invoker.
func TestScheduledTask_RunsThroughTheProductionInvoker(t *testing.T) {
	db, creds := startProjectPG(t)
	if err := scheduler.EnsureTables(context.Background(), db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	opener := wiringOpener(t, creds, "ACTIVE")

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	h := deployHandler(t, opener, runtime.URL, "ACTIVE")
	deployModule(t, h, wiringProjectID, "jobs")

	if _, err := db.Exec(`
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ('task_wiring', $1, 'jobs', 'send', '{"to":"ada"}', now() - interval '1 second', 'pending')
	`, wiringProjectID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	fanout := scheduler.NewFanout(scheduler.FanoutConfig{
		Projects:   opener.ServableProjectIDs,
		DB:         opener.Open,
		Invoker:    h.SchedulerInvoker(),
		Functions:  h.SchedulerFunctions(),
		CronLeader: alwaysLeaderForTest{},
	})
	if err := fanout.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("runtime calls: got %d, want the scheduled dispatch", len(received))
	}
	if received[0].Path != "/invoke/"+wiringProjectID+"__jobs" {
		t.Errorf("path: got %q", received[0].Path)
	}
	if received[0].Secret == "" {
		t.Error("the scheduled dispatch carried no runtime credential")
	}
	if got := taskStatus(t, db, "task_wiring"); got != "completed" {
		t.Errorf("task status: got %q, want completed", got)
	}
}

// A cron entry deployed into the tenant database is enqueued by the leader
// and then dispatched by the next task sweep.
func TestCronEntry_IsEnqueuedAndDispatched(t *testing.T) {
	db, creds := startProjectPG(t)
	if err := scheduler.EnsureTables(context.Background(), db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	opener := wiringOpener(t, creds, "ACTIVE")

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	h := deployHandler(t, opener, runtime.URL, "ACTIVE")
	deployModule(t, h, wiringProjectID, "jobs")

	if _, err := db.Exec(`
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, function_id, module_name, export_name, args, schedule)
		VALUES ('sweep', $1, 'wiring-app', 'jobs', 'sweep', '{}', '{"kind":"interval","seconds":1}')
	`, wiringProjectID); err != nil {
		t.Fatalf("seed cron: %v", err)
	}

	fanout := scheduler.NewFanout(scheduler.FanoutConfig{
		Projects:   opener.ServableProjectIDs,
		DB:         opener.Open,
		Invoker:    h.SchedulerInvoker(),
		Functions:  h.SchedulerFunctions(),
		CronLeader: alwaysLeaderForTest{},
	})
	if err := fanout.CronTick(context.Background()); err != nil {
		t.Fatalf("CronTick: %v", err)
	}

	var pending int
	if err := db.QueryRow(`
		SELECT count(*) FROM excalibase.excalibase_scheduled_functions
		 WHERE project_id = $1 AND status = 'pending'`, wiringProjectID).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("cron enqueued %d tasks, want 1", pending)
	}

	// The enqueued task is due a second out; move it into the past so one
	// deterministic sweep dispatches it.
	if _, err := db.Exec(`
		UPDATE excalibase.excalibase_scheduled_functions SET scheduled_for = now() - interval '1 second'`); err != nil {
		t.Fatalf("age task: %v", err)
	}
	if err := fanout.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("runtime calls: got %d, want the cron dispatch", len(received))
	}
}

// A project the platform must not serve keeps its tasks: nothing is
// dispatched and the row is closed as skipped rather than retried.
func TestScheduledTask_NotServableProjectIsSkipped(t *testing.T) {
	db, creds := startProjectPG(t)
	if err := scheduler.EnsureTables(context.Background(), db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	opener := wiringOpener(t, creds, string(domain.StatusDeleting))

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	h := deployHandler(t, opener, runtime.URL, string(domain.StatusDeleting))
	deployModule(t, h, wiringProjectID, "jobs")

	if _, err := db.Exec(`
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ('task_gone', $1, 'jobs', 'send', '{}', now() - interval '1 second', 'pending')
	`, wiringProjectID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	// The sweep never lists a project under teardown, so drive the worker
	// directly: this is the race where the project is claimed between the
	// listing and the dispatch.
	worker := scheduler.NewWorker(scheduler.WorkerConfig{
		DB:        db,
		ProjectID: wiringProjectID,
		Invoker:   h.SchedulerInvoker(),
		Functions: h.SchedulerFunctions(),
	})
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(received) != 0 {
		t.Errorf("a project under teardown was invoked: %v", received)
	}
	if got := taskStatus(t, db, "task_gone"); got != "skipped" {
		t.Errorf("task status: got %q, want skipped", got)
	}

	ids, err := opener.ServableProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("a project under teardown is still swept: %v", ids)
	}
}

type alwaysLeaderForTest struct{}

func (alwaysLeaderForTest) IsLeader(context.Context) (bool, error) { return true, nil }

func taskStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(
		`SELECT status FROM excalibase.excalibase_scheduled_functions WHERE id = $1`, id,
	).Scan(&status); err != nil {
		t.Fatalf("read task %s: %v", id, err)
	}
	return status
}

// The tenant owns its own database, so it can write any project id into its
// queue. The victim's runtime must never see it, and the row is closed as
// failed with the platform's own reason.
func TestScheduledTask_RowNamingAnotherProjectNeverReachesThatProject(t *testing.T) {
	db, creds := startProjectPG(t)
	if err := scheduler.EnsureTables(context.Background(), db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}

	const victimID = "proj_victim"
	instances := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		wiringProjectID: {ProjectID: wiringProjectID, OrgID: "default", Status: "ACTIVE"},
		victimID:        {ProjectID: victimID, OrgID: "default", Status: "ACTIVE"},
	}}
	vault := &e2eFakeVault{data: map[string]map[string]string{
		fmt.Sprintf("projects/%s/credentials/excalibase_app", wiringProjectID): creds,
		fmt.Sprintf("projects/%s/credentials/excalibase_app", victimID):        creds,
	}}
	opener := projectdb.NewOpener(instances, vault, projectdb.Overrides{SSLMode: "disable"}, projectdb.PoolLimits{})
	defer opener.Close()

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	store := edgefn.NewFunctionStore(t.TempDir())
	client := edgefn.NewRuntimeClient(runtime.URL, "runtime-shared")
	h := NewFunctionHandler(store, nil, client, instances, nil, "")
	h.SetProjectDBFn(opener.Open)
	// The victim really does have this function deployed — the refusal must
	// not depend on the module being unknown.
	deployModule(t, h, victimID, "jobs")
	deployModule(t, h, wiringProjectID, "jobs")

	if _, err := db.Exec(`
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ('task_cross', $1, 'jobs', 'send', '{"drain":"victim"}', now() - interval '1 second', 'pending')
	`, victimID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	fanout := scheduler.NewFanout(scheduler.FanoutConfig{
		// The sweep only ever hands the worker the project whose database it
		// opened; here that is the attacker's project.
		Projects:   func(context.Context) ([]string, error) { return []string{wiringProjectID}, nil },
		DB:         opener.Open,
		Invoker:    h.SchedulerInvoker(),
		Functions:  h.SchedulerFunctions(),
		CronLeader: alwaysLeaderForTest{},
	})
	if err := fanout.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}

	if len(received) != 0 {
		t.Fatalf("a row naming another project reached a runtime: %v", received)
	}
	if got := taskStatus(t, db, "task_cross"); got != "failed" {
		t.Errorf("task status: got %q, want failed", got)
	}
}

// A row naming a module the platform never deployed is closed too: the
// module name is tenant input, checked against the platform's own registry.
func TestScheduledTask_UndeployedModuleIsRefused(t *testing.T) {
	db, creds := startProjectPG(t)
	if err := scheduler.EnsureTables(context.Background(), db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	opener := wiringOpener(t, creds, "ACTIVE")

	var received []runtimeCall
	runtime := fakeRuntime(t, http.StatusOK, &received)
	h := deployHandler(t, opener, runtime.URL, "ACTIVE")

	if _, err := db.Exec(`
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ('task_ghost', $1, 'never-deployed', 'send', '{}', now() - interval '1 second', 'pending')
	`, wiringProjectID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	fanout := scheduler.NewFanout(scheduler.FanoutConfig{
		Projects:   opener.ServableProjectIDs,
		DB:         opener.Open,
		Invoker:    h.SchedulerInvoker(),
		Functions:  h.SchedulerFunctions(),
		CronLeader: alwaysLeaderForTest{},
	})
	if err := fanout.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if len(received) != 0 {
		t.Fatalf("an undeployed module was dispatched: %v", received)
	}
	if got := taskStatus(t, db, "task_ghost"); got != "failed" {
		t.Errorf("task status: got %q, want failed", got)
	}
}
