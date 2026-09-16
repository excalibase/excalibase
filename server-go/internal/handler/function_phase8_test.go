//go:build integration

// Phase 8.5 — cron-job table sync at deploy time.
//
// When a function bundle declares `cronJobs()` the Go bundler extracts the
// registry into Function.CronJobs (already wired by Phase 8). What is still
// missing — and what these tests pin down — is the sync from that captured
// JSON to the `excalibase_cron_jobs` table in the project DB at deploy
// time. The Cron runner reads that table, so without the sync nothing
// actually runs.
//
// Tests:
//   - new bundle with 2 jobs → 2 rows
//   - re-deploy that adds one + removes one → table matches latest bundle
//   - re-deploy that drops the cron registry entirely → table is empty
//   - the sync is transactional: if the runtime deploy fails, the rows
//     don't land (rollback) so the table never drifts from the persisted
//     Function record.
package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupCronPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("fn_cron_test"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	connStr, _ := c.ConnectionString(ctx, "sslmode=disable")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := scheduler.EnsureTables(ctx, db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	return db, func() {
		db.Close()
		_ = c.Terminate(ctx)
	}
}

// mockEmptyRuntime stands in for the Deno runtime when we just need a
// 201 on /deploy and 200 on /health — we're testing the cron-table sync,
// not the runtime hand-off.
func mockEmptyRuntime(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			w.Write([]byte(`{"status":"healthy"}`))
		case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"ok"}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func buildPhase8Router(t *testing.T, db *sql.DB) (*chi.Mux, *edgefn.FunctionStore) {
	t.Helper()
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	vault := &e2eFakeVault{data: map[string]map[string]string{}}
	secrets := edgefn.NewSecretsStore(vault)
	runtime := mockEmptyRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_cron0001": {ProjectID: "proj_cron0001", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "https://api.cron.test")
	h.SetProjectDBFn(func(_ context.Context, _ string) (*sql.DB, error) {
		return db, nil
	})
	h.SetAutoMigrate(true)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Post("/", h.Create)
	})
	return r, store
}

// deployFn POSTs a Function body to the test router and returns the
// recorded response. Helper so each test stays focused on its assertion.
func deployFn(t *testing.T, r chi.Router, projectID, fnID, indexContent string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]interface{}{
		"id":   fnID,
		"name": fnID,
		"files": []map[string]string{
			{"path": "index.ts", "content": indexContent},
		},
	}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/api/projects/"+projectID+"/functions/", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func listCronRows(t *testing.T, db *sql.DB, projectID string) []map[string]any {
	t.Helper()
	rows, err := db.Query(`
		SELECT name, project_id, module_name, export_name, args, schedule
		  FROM excalibase.excalibase_cron_jobs
		 WHERE project_id = $1
		 ORDER BY name`, projectID)
	if err != nil {
		t.Fatalf("query cron rows: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var (
			name, pid, mod, exp string
			args, sched         []byte
		)
		if err := rows.Scan(&name, &pid, &mod, &exp, &args, &sched); err != nil {
			t.Fatalf("scan cron row: %v", err)
		}
		out = append(out, map[string]any{
			"name":        name,
			"project_id":  pid,
			"module_name": mod,
			"export_name": exp,
			"args":        json.RawMessage(args),
			"schedule":    json.RawMessage(sched),
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// Bundle content: a v2 mutation default export plus a cronJobs side-channel
// with `entries` jobs. Mirrors what `@excalibase/server` cronJobs() emits.
func cronBundle(entries string) string {
	return `
globalThis.__excalibase_crons = [` + entries + `];
export default {
  kind: "mutation",
  args: { parse: (a) => a },
  handler: async (_ctx, _args) => null,
  __metadata: { argsJsonSchema: { type: "object", properties: {} } },
};
`
}

// TestDeploy_SyncsCronJobsToDB — a fresh deploy with 2 cron entries must
// produce 2 rows in `excalibase_cron_jobs` keyed by (project_id, name).
func TestDeploy_SyncsCronJobsToDB(t *testing.T) {
	db, cleanup := setupCronPG(t)
	defer cleanup()
	r, _ := buildPhase8Router(t, db)

	indexTS := cronBundle(`
{ name: "daily-digest",
  schedule: { kind: "daily", hourUTC: 9, minuteUTC: 0 },
  fnRef: { moduleName: "jobs", exportName: "sendDigest" },
  args: { who: "all" } },
{ name: "every-5",
  schedule: { kind: "cron", expression: "*/5 * * * *" },
  fnRef: { moduleName: "jobs", exportName: "tick" },
  args: { n: 1 } }
`)

	w := deployFn(t, r, "proj_cron0001", "v2crons", indexTS)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: got %d, body=%s", w.Code, w.Body.String())
	}
	rows := listCronRows(t, db, "proj_cron0001")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows after deploy, got %d (%+v)", len(rows), rows)
	}
	if rows[0]["name"] != "daily-digest" {
		t.Errorf("rows[0].name: got %v, want daily-digest", rows[0]["name"])
	}
	if rows[0]["module_name"] != "jobs" || rows[0]["export_name"] != "sendDigest" {
		t.Errorf("rows[0] fnRef wrong: %+v", rows[0])
	}
	if rows[1]["name"] != "every-5" {
		t.Errorf("rows[1].name: got %v, want every-5", rows[1]["name"])
	}
}

// TestDeploy_UpdatesCronJobsOnRedeploy — re-deploying with a different set
// (one removed, one added, one with changed schedule) must leave the table
// matching the new bundle exactly.
func TestDeploy_UpdatesCronJobsOnRedeploy(t *testing.T) {
	db, cleanup := setupCronPG(t)
	defer cleanup()
	r, _ := buildPhase8Router(t, db)

	first := cronBundle(`
{ name: "a", schedule: { kind: "hourly", minuteUTC: 0 },
  fnRef: { moduleName: "jobs", exportName: "a" }, args: {} },
{ name: "b", schedule: { kind: "daily", hourUTC: 6, minuteUTC: 0 },
  fnRef: { moduleName: "jobs", exportName: "b" }, args: {} }
`)
	w := deployFn(t, r, "proj_cron0001", "fn1", first)
	if w.Code != http.StatusCreated {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	if got := len(listCronRows(t, db, "proj_cron0001")); got != 2 {
		t.Fatalf("first deploy rows: got %d, want 2", got)
	}

	// Second deploy of the SAME function: drop "a", keep "b" with changed
	// schedule, add "c". The (project_id, name) primary key means the sync
	// must DELETE "a", UPDATE "b", and INSERT "c".
	second := cronBundle(`
{ name: "b", schedule: { kind: "daily", hourUTC: 18, minuteUTC: 30 },
  fnRef: { moduleName: "jobs", exportName: "b" }, args: { v: 2 } },
{ name: "c", schedule: { kind: "interval", minutes: 10 },
  fnRef: { moduleName: "jobs", exportName: "c" }, args: {} }
`)
	w = deployFn(t, r, "proj_cron0001", "fn1", second)
	if w.Code != http.StatusCreated {
		t.Fatalf("second deploy: %d %s", w.Code, w.Body.String())
	}
	rows := listCronRows(t, db, "proj_cron0001")
	if len(rows) != 2 {
		t.Fatalf("redeploy rows: got %d, want 2 (%+v)", len(rows), rows)
	}
	names := []string{rows[0]["name"].(string), rows[1]["name"].(string)}
	if names[0] != "b" || names[1] != "c" {
		t.Fatalf("redeploy names: got %v, want [b c]", names)
	}
	// "b" must have the new schedule (hourUTC=18).
	var sched map[string]any
	if err := json.Unmarshal(rows[0]["schedule"].(json.RawMessage), &sched); err != nil {
		t.Fatalf("unmarshal b schedule: %v", err)
	}
	// JSON numbers decode as float64 — accept either.
	hr := sched["hourUTC"]
	if hr != float64(18) && hr != 18 {
		t.Errorf("b.hourUTC: got %v, want 18", hr)
	}
}

// TestDeploy_ClearsCronJobsWhenBundleDropsThem — a redeploy whose bundle
// no longer calls cronJobs() must clear ALL rows for that function. We use
// a per-function key (currently project_id+name) so deleting all jobs for
// a function name = delete rows whose name appeared in the prior bundle.
func TestDeploy_ClearsCronJobsWhenBundleDropsThem(t *testing.T) {
	db, cleanup := setupCronPG(t)
	defer cleanup()
	r, _ := buildPhase8Router(t, db)

	first := cronBundle(`
{ name: "x", schedule: { kind: "hourly", minuteUTC: 15 },
  fnRef: { moduleName: "jobs", exportName: "x" }, args: {} }
`)
	w := deployFn(t, r, "proj_cron0001", "fn2", first)
	if w.Code != http.StatusCreated {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	if got := len(listCronRows(t, db, "proj_cron0001")); got != 1 {
		t.Fatalf("first deploy rows: %d", got)
	}

	// Same function, no cron registry → table must be empty after deploy.
	noCron := `
export default {
  kind: "mutation",
  args: { parse: (a) => a },
  handler: async (_ctx, _args) => null,
  __metadata: { argsJsonSchema: { type: "object", properties: {} } },
};
`
	w = deployFn(t, r, "proj_cron0001", "fn2", noCron)
	if w.Code != http.StatusCreated {
		t.Fatalf("redeploy: %d %s", w.Code, w.Body.String())
	}
	if got := len(listCronRows(t, db, "proj_cron0001")); got != 0 {
		t.Fatalf("after no-cron redeploy: got %d rows, want 0", got)
	}
}

// TestDeploy_CronSyncRollsBackOnDeployFailure — if the runtime hand-off
// fails after the cron rows go in, the rows must roll back so the persisted
// table never diverges from the function record (which the handler also
// rolls back).
func TestDeploy_CronSyncRollsBackOnDeployFailure(t *testing.T) {
	db, cleanup := setupCronPG(t)
	defer cleanup()

	// Build a router whose runtime always 500s on /deploy so the handler
	// hits the rollback path.
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	vault := &e2eFakeVault{data: map[string]map[string]string{}}
	secrets := edgefn.NewSecretsStore(vault)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"status":"healthy"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"deploy boom"}`))
	}))
	defer failing.Close()
	client := edgefn.NewRuntimeClient(failing.URL, "")
	instStore := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_cron0001": {ProjectID: "proj_cron0001", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "https://api.cron.test")
	h.SetProjectDBFn(func(_ context.Context, _ string) (*sql.DB, error) { return db, nil })
	h.SetAutoMigrate(true)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Post("/", h.Create)
	})

	indexTS := cronBundle(`
{ name: "should-rollback", schedule: { kind: "hourly", minuteUTC: 0 },
  fnRef: { moduleName: "jobs", exportName: "x" }, args: {} }
`)
	w := deployFn(t, r, "proj_cron0001", "fn3", indexTS)
	if w.Code == http.StatusCreated {
		t.Fatalf("deploy unexpectedly succeeded against failing runtime: %s", w.Body.String())
	}
	// Cron table must be empty — the deploy failed, the sync must roll back.
	if rows := listCronRows(t, db, "proj_cron0001"); len(rows) != 0 {
		t.Fatalf("cron rows must roll back on deploy failure; got %d (%+v)", len(rows), rows)
	}
}
