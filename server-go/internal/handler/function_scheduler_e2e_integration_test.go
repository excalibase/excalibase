//go:build integration

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

// End to end: a function's ctx.scheduler calls go from the real Deno runtime
// to the real provisioning route and land in the tenant database as the
// scheduler's login. The runtime's own database login is the project owner's,
// which cannot write the platform's scheduler tables (EXC-623).

const schedulerE2EMaster = "scheduler-e2e-master"

const schedulerE2EFunctions = `globalThis.__excalibase_default = {
  kind: "mutation",
  args: { parse: (a) => a },
  handler: async (ctx, args) => {
    if (args.cancel) {
      await ctx.scheduler.cancel(args.cancel);
      return { cancelled: args.cancel };
    }
    const kept = await ctx.scheduler.runAfter(60000, { moduleName: "jobs", exportName: "send" }, { to: "ada" });
    const dropped = await ctx.scheduler.runAt(Date.now() + 120000, { moduleName: "jobs", exportName: "send" }, {});
    await ctx.scheduler.cancel(dropped);
    if (args.fail) throw new Error("rolled back after scheduling");
    return { kept, dropped };
  },
};`

func TestFunctionScheduler_ScheduleAndCancelEndToEnd(t *testing.T) {
	db, logins := startProjectPG(t)
	mustExecE2E(t, db, `CREATE ROLE tenant_owner LOGIN PASSWORD 'owner-pass'`)
	owner := logins["excalibase_app"]
	ownerLogin := map[string]string{"host": owner["host"], "port": owner["port"], "username": "tenant_owner", "password": "owner-pass", "database": "tenant"}
	ownerDB := openWiringDB(t, ownerLogin)
	if _, err := ownerDB.Exec(`INSERT INTO excalibase.excalibase_scheduled_functions
		(id, project_id, module_name, export_name, args, scheduled_for) VALUES ('direct', $1, 'jobs', 'send', '{}', now())`, wiringProjectID); err == nil {
		t.Fatal("the project's own login wrote the scheduler table")
	}

	opener := wiringOpener(t, logins, "ACTIVE")
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, nil, nil, "")
	h.runtimeSecret = schedulerE2EMaster
	h.SetSchedulerDBFn(opener.OpenScheduler)
	router := chi.NewRouter()
	router.Post("/internal/runtime/projects/{projectId}/scheduler", h.InternalSchedule)
	provisioning := httptest.NewServer(router)
	t.Cleanup(provisioning.Close)

	runtime := startSchedulerE2ERuntime(t, map[string]string{
		"RUNTIME_SECRET":              edgefn.DeriveRuntimeSecret(schedulerE2EMaster, wiringProjectID),
		"EXCALIBASE_DB_URL":           fmt.Sprintf("postgres://tenant_owner:owner-pass@%s:%s/tenant?sslmode=disable", owner["host"], owner["port"]),
		"EXCALIBASE_PROVISIONING_URL": provisioning.URL,
	})
	fnID := wiringProjectID + "__caller"
	runtime.deploy(t, fnID, schedulerE2EFunctions)

	var scheduled struct{ Kept, Dropped string }
	runtime.invoke(t, fnID, map[string]any{}, &scheduled)
	if got := taskStatus(t, db, scheduled.Kept); got != "pending" {
		t.Errorf("the scheduled task is %q, want pending", got)
	}
	if got := taskStatus(t, db, scheduled.Dropped); got != "cancelled" {
		t.Errorf("the task cancelled in the same mutation is %q, want cancelled", got)
	}
	var projectID string
	if err := db.QueryRow(`SELECT project_id FROM excalibase.excalibase_scheduled_functions WHERE id = $1`, scheduled.Kept).Scan(&projectID); err != nil || projectID != wiringProjectID {
		t.Errorf("the task belongs to %q (%v), want %q", projectID, err, wiringProjectID)
	}

	runtime.invoke(t, fnID, map[string]any{"cancel": scheduled.Kept}, nil)
	if got := taskStatus(t, db, scheduled.Kept); got != "cancelled" {
		t.Errorf("a later cancel left the task %q", got)
	}

	before := countScheduled(t, db)
	runtime.invokeRaw(t, fnID, map[string]any{"fail": true})
	if after := countScheduled(t, db); after != before {
		t.Errorf("a rolled-back mutation recorded %d task(s)", after-before)
	}
}

func mustExecE2E(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

func countScheduled(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM excalibase.excalibase_scheduled_functions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type schedulerE2ERuntime struct {
	base   string
	secret string
}

// startSchedulerE2ERuntime runs deno-server/server.ts on a free port.
func startSchedulerE2ERuntime(t *testing.T, env map[string]string) *schedulerE2ERuntime {
	t.Helper()
	deno := findDeno()
	if deno == "" {
		t.Skip("deno binary not found")
	}
	wd, _ := os.Getwd()
	serverTS := filepath.Clean(filepath.Join(wd, "..", "..", "..", "deno-server", "server.ts"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	cmd := exec.Command(deno, "run", "--allow-env", "--allow-net", "--allow-read", "--unstable-worker-options", serverTS)
	cmd.Env = append(os.Environ(), "PORT="+fmt.Sprint(port), "EXCALIBASE_FUNCTIONS_V2=1", "EXCALIBASE_SHUTDOWN_DRAIN_MS=0")
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start deno: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	rt := &schedulerE2ERuntime{base: fmt.Sprintf("http://127.0.0.1:%d", port), secret: env["RUNTIME_SECRET"]}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(rt.base + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return rt
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("deno runtime did not become healthy")
	return nil
}

func (rt *schedulerE2ERuntime) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, rt.base+path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(testRuntimeSecretHeader, rt.secret)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (rt *schedulerE2ERuntime) deploy(t *testing.T, id, code string) {
	t.Helper()
	if status, body := rt.post(t, "/deploy", map[string]any{"id": id, "code": code}); status != http.StatusCreated {
		t.Fatalf("deploy: %d %s", status, body)
	}
}

// invokeRaw returns the runtime's InvokeResponse as it came.
func (rt *schedulerE2ERuntime) invokeRaw(t *testing.T, id string, args map[string]any) (int, string) {
	t.Helper()
	argBody, _ := json.Marshal(map[string]any{"args": args})
	status, body := rt.post(t, "/invoke/"+id, map[string]any{
		"method": "POST", "url": "/invoke/" + id, "headers": map[string]string{}, "body": string(argBody),
	})
	if status != http.StatusOK {
		t.Fatalf("invoke transport: %d %s", status, body)
	}
	var res struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("invoke response %s: %v", body, err)
	}
	return res.Status, res.Body
}

func (rt *schedulerE2ERuntime) invoke(t *testing.T, id string, args map[string]any, into any) {
	t.Helper()
	status, body := rt.invokeRaw(t, id, args)
	if status != http.StatusOK || strings.Contains(body, `"error"`) {
		t.Fatalf("invoke %s: %d %s", id, status, body)
	}
	if into == nil {
		return
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("invoke body %s: %v", body, err)
	}
	if err := json.Unmarshal(envelope.Data, into); err != nil {
		t.Fatalf("invoke data %s: %v", envelope.Data, err)
	}
}
