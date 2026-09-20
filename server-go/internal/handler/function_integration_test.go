//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	testE2ESecret           = "handler-e2e-secret"
	testRuntimeSecretHeader = "X-Runtime-Secret"
	testE2EFnPath           = "/api/projects/proj_e2e01/functions/"
	testDeployFmt           = "deploy: %d, body=%s"
	testInvokeFmt           = "invoke: %d, body=%s"
	testE2ESecretsPath      = "/api/projects/proj_e2e01/functions/secrets"
	testE2EInvokePath       = "/api/projects/proj_e2e01/functions/echo-key/invoke"
)

// Full stack e2e: Go handler → RuntimeClient → real Deno runtime subprocess.
// Tests the HTTP layer all the way from platform API to user code and back.
//
// Run: go test -tags=integration ./internal/handler/ -run TestFnE2E -v

func findDeno() string {
	if p, err := exec.LookPath("deno"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, ".deno", "bin", "deno")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func startDenoRuntime(t *testing.T) (string, func()) {
	t.Helper()
	deno := findDeno()
	if deno == "" {
		t.Skip("deno binary not found")
	}
	wd, _ := os.Getwd()
	serverTS := filepath.Clean(filepath.Join(wd, "..", "..", "..", "deno-server", "server.ts"))
	if _, err := os.Stat(serverTS); err != nil {
		t.Skipf("deno-server/server.ts not found at %s", serverTS)
	}
	secret := testE2ESecret
	cmd := exec.Command(deno, "run",
		"--allow-env", "--allow-net", "--allow-read",
		"--unstable-worker-options",
		serverTS,
	)
	cmd.Env = append(os.Environ(), "RUNTIME_SECRET="+secret)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start deno: %v", err)
	}
	go io.Copy(os.Stderr, stderr)
	go io.Copy(os.Stdout, stdout)

	base := "http://127.0.0.1:8000"
	kill := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", base+"/health", nil)
		req.Header.Set(testRuntimeSecretHeader, secret)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return base, kill
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	kill()
	t.Fatal("deno runtime did not become healthy")
	return "", nil
}

// lightweight instance/secrets plumbing (we're not testing storage here)
type e2eInstanceStore struct {
	insts map[string]*domain.DatabaseInstance
}

func (s *e2eInstanceStore) Create(inst *domain.DatabaseInstance) error {
	if _, taken := s.insts[inst.ProjectID]; taken {
		return storage.ErrProjectExists
	}
	s.insts[inst.ProjectID] = inst
	return nil
}
func (s *e2eInstanceStore) Update(inst *domain.DatabaseInstance) error {
	existing, ok := s.insts[inst.ProjectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	updated := *inst
	updated.OrgID = existing.OrgID
	s.insts[inst.ProjectID] = &updated
	return nil
}
func (s *e2eInstanceStore) FindByProjectID(id string) (*domain.DatabaseInstance, error) {
	return s.insts[id], nil
}
func (s *e2eInstanceStore) FindAll() ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0, len(s.insts))
	for _, v := range s.insts {
		out = append(out, v)
	}
	return out, nil
}
func (s *e2eInstanceStore) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0)
	for _, v := range s.insts {
		if v.OwnerID == ownerID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *e2eInstanceStore) Delete(id string) error { delete(s.insts, id); return nil }

func (s *e2eInstanceStore) BeginDeletion(projectID string, deleteBackups *bool) (bool, error) {
	inst, ok := s.insts[projectID]
	if !ok {
		return false, storage.ErrProjectNotFound
	}
	return storage.ApplyBeginDeletion(inst, deleteBackups)
}

func (s *e2eInstanceStore) RecordPauseAttempt(string, time.Time) (int, error) {
	return 0, nil
}

func (s *e2eInstanceStore) RecordRestoreInterrupted(string, string, string) error {
	return nil
}

func (s *e2eInstanceStore) RecordDeletionFailure(projectID string, status domain.ProvisioningStage, step, reason string) error {
	inst, ok := s.insts[projectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	return storage.ApplyDeletionFailure(inst, status, step, reason)
}

type e2eFakeVault struct{ data map[string]map[string]string }

func (f *e2eFakeVault) Get(p string) (map[string]string, error) {
	if d, ok := f.data[p]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("not found")
}
func (f *e2eFakeVault) Put(p string, d map[string]string) error {
	cp := make(map[string]string, len(d))
	for k, v := range d {
		cp[k] = v
	}
	f.data[p] = cp
	return nil
}
func (f *e2eFakeVault) Delete(p string) error                { delete(f.data, p); return nil }
func (f *e2eFakeVault) List(prefix string) ([]string, error) { return nil, nil }
func (f *e2eFakeVault) DeletePrefix(prefix string) (int, error) {
	n := 0
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}
func (f *e2eFakeVault) Sealed() bool                  { return false }
func (f *e2eFakeVault) GetPublicKey() (string, error) { return "", nil }

func setupFnE2E(t *testing.T) (*chi.Mux, string, func()) {
	t.Helper()
	base, cleanup := startDenoRuntime(t)

	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	vault := &e2eFakeVault{data: map[string]map[string]string{}}
	secrets := edgefn.NewSecretsStore(vault)
	client := edgefn.NewRuntimeClient(base, testE2ESecret)

	instStore := &e2eInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_e2e01": {ProjectID: "proj_e2e01", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "https://api.e2e.test")

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Post("/secrets", h.SetSecret)
		r.Delete("/secrets/{key}", h.DeleteSecret)
		r.Route("/{fnId}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Delete("/", h.Delete)
			r.Post("/invoke", h.Invoke)
			r.Get("/logs", h.Logs)
		})
	})
	return r, base, cleanup
}

func doReq(r chi.Router, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(context.Background()))
	return w
}

// --- TESTS ---

func TestFnE2E_Deploy_Invoke_FullChain(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	// Deploy via API
	body := map[string]interface{}{
		"id":   "greet",
		"name": "Greet",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default async (req: Request): Promise<Response> => {
  const { name = 'anon' } = await req.json().catch(() => ({}));
  return Response.json({ hello: name });
};`},
		},
	}
	w := doReq(r, "POST", testE2EFnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}

	// Invoke
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/greet/invoke", map[string]string{"name": "world"})
	if w.Code != 200 {
		t.Fatalf(testInvokeFmt, w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if out["hello"] != "world" {
		t.Errorf("hello: %q", out["hello"])
	}
}

func TestFnE2E_Secrets_VisibleToFunction(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	// Set a secret first
	w := doReq(r, "POST", testE2ESecretsPath,
		map[string]string{"key": "API_TOKEN", "value": "tok_sekrit"})
	if w.Code != 200 {
		t.Fatalf("set secret: %d, body=%s", w.Code, w.Body.String())
	}

	// Deploy a function that reads the secret
	body := map[string]interface{}{
		"id":   "read-env",
		"name": "Read Env",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default (req: Request) => Response.json({
  token: Deno.env.get('API_TOKEN'),
  url: Deno.env.get('EXCALIBASE_URL'),
  pid: Deno.env.get('EXCALIBASE_PROJECT_ID'),
});`},
		},
	}
	w = doReq(r, "POST", testE2EFnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}

	// Invoke → function should see the secret + built-ins
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/read-env/invoke", map[string]string{})
	if w.Code != 200 {
		t.Fatalf(testInvokeFmt, w.Code, w.Body.String())
	}
	var got map[string]string
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["token"] != "tok_sekrit" {
		t.Errorf("API_TOKEN not injected: %+v", got)
	}
	if got["pid"] != "proj_e2e01" {
		t.Errorf("EXCALIBASE_PROJECT_ID not injected: %+v", got)
	}
	if got["url"] != "https://api.e2e.test/functions/v1/proj_e2e01" {
		t.Errorf("EXCALIBASE_URL: %q", got["url"])
	}
}

func TestFnE2E_SecretChangeTakesEffectAfterSet(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	// Deploy a function that just echoes a secret
	body := map[string]interface{}{
		"id": "echo-key", "name": "Echo Key",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default (req: Request) => Response.json({ v: Deno.env.get('ROTATE_ME') || 'unset' });`},
		},
	}
	w := doReq(r, "POST", testE2EFnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d", w.Code)
	}

	// Invoke → should be 'unset'
	w = doReq(r, "POST", testE2EInvokePath, map[string]string{})
	var r1 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r1)
	if r1["v"] != "unset" {
		t.Errorf("pre-set: %q, want 'unset'", r1["v"])
	}

	// Set secret — handler redeploys automatically
	w = doReq(r, "POST", testE2ESecretsPath,
		map[string]string{"key": "ROTATE_ME", "value": "first-value"})
	if w.Code != 200 {
		t.Fatalf("set secret: %d, body=%s", w.Code, w.Body.String())
	}

	// Invoke again → should be 'first-value'
	w = doReq(r, "POST", testE2EInvokePath, map[string]string{})
	var r2 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r2)
	if r2["v"] != "first-value" {
		t.Errorf("after set: %q, want 'first-value'", r2["v"])
	}

	// Rotate the value
	w = doReq(r, "POST", testE2ESecretsPath,
		map[string]string{"key": "ROTATE_ME", "value": "second-value"})
	if w.Code != 200 {
		t.Fatalf("rotate secret: %d", w.Code)
	}
	w = doReq(r, "POST", testE2EInvokePath, map[string]string{})
	var r3 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r3)
	if r3["v"] != "second-value" {
		t.Errorf("after rotate: %q, want 'second-value'", r3["v"])
	}
}

func TestFnE2E_LogStreaming_CapturesConsoleCalls(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	body := map[string]interface{}{
		"id": "logger", "name": "Logger",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default (req: Request) => {
  console.log('hello from user code', { who: 'invoker' });
  console.warn('something smells off');
  console.error('big problem');
  return Response.json({ ok: 1 });
};`},
		},
	}
	w := doReq(r, "POST", testE2EFnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}

	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/logger/invoke", map[string]string{})
	if w.Code != 200 {
		t.Fatalf(testInvokeFmt, w.Code, w.Body.String())
	}

	// Small wait so the worker's postMessage logs land in the ring buffer.
	time.Sleep(250 * time.Millisecond)

	w = doReq(r, "GET", "/api/projects/proj_e2e01/functions/logger/logs", nil)
	if w.Code != 200 {
		t.Fatalf("logs: %d, body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Logs []edgefn.LogEntry `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	if len(result.Logs) < 3 {
		t.Fatalf("expected >= 3 log entries, got %d: %+v", len(result.Logs), result.Logs)
	}

	assertConsoleLevelsPresent(t, result.Logs)
	assertSinceFilterEmpty(t, r, result.Logs, "/api/projects/proj_e2e01/functions/logger/logs")
}

// assertConsoleLevelsPresent verifies that log, warn, and error entries are all present.
func assertConsoleLevelsPresent(t *testing.T, logs []edgefn.LogEntry) {
	t.Helper()
	seenLog, seenWarn, seenError := false, false, false
	for _, l := range logs {
		switch l.Level {
		case "log":
			if strings.Contains(l.Msg, "hello from user code") {
				seenLog = true
			}
		case "warn":
			if strings.Contains(l.Msg, "something smells off") {
				seenWarn = true
			}
		case "error":
			if strings.Contains(l.Msg, "big problem") {
				seenError = true
			}
		}
	}
	if !seenLog || !seenWarn || !seenError {
		t.Errorf("missing log levels — log=%v warn=%v error=%v: %+v", seenLog, seenWarn, seenError, logs)
	}
}

// assertSinceFilterEmpty verifies that querying logs with since=<latestTS> returns no entries.
func assertSinceFilterEmpty(t *testing.T, r chi.Router, logs []edgefn.LogEntry, logsPath string) {
	t.Helper()
	latestTS := int64(0)
	for _, l := range logs {
		if l.TS > latestTS {
			latestTS = l.TS
		}
	}
	w := doReq(r, "GET", fmt.Sprintf("%s?since=%d", logsPath, latestTS), nil)
	if w.Code != 200 {
		t.Fatalf("logs since: %d", w.Code)
	}
	var sinceResult struct {
		Logs []edgefn.LogEntry `json:"logs"`
	}
	json.Unmarshal(w.Body.Bytes(), &sinceResult)
	if len(sinceResult.Logs) != 0 {
		t.Errorf("since=latest should return no logs, got %d", len(sinceResult.Logs))
	}
}

func TestFnE2E_InfiniteLoopWorkerTerminated(t *testing.T) {
	r, base, cleanup := setupFnE2E(t)
	defer cleanup()

	// Deploy a function that loops forever — should be killed, not hang.
	body := map[string]interface{}{
		"id": "looper", "name": "Infinite Loop",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default () => { while(true){} };`},
		},
	}
	w := doReq(r, "POST", testE2EFnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy looper: %d, body=%s", w.Code, w.Body.String())
	}

	// Invoke the infinite loop — should error within the timeout (not hang forever).
	// The runtime should terminate the worker after the invoke timeout.
	start := time.Now()
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/looper/invoke", map[string]string{})
	elapsed := time.Since(start)
	if w.Code == 200 {
		t.Fatal("infinite loop should not return 200")
	}
	// Should timeout within ~35s (30s invoke timeout + overhead), not hang.
	if elapsed > 45*time.Second {
		t.Errorf("invoke took %v — should have timed out around 30s", elapsed)
	}

	// After timeout, the worker should be terminated and removed from the
	// runtime's script map. Verify via the /scripts endpoint.
	time.Sleep(500 * time.Millisecond)
	client := &http.Client{Timeout: 5 * time.Second}
	scriptsReq, _ := http.NewRequest("GET", base+"/scripts", nil)
	scriptsReq.Header.Set(testRuntimeSecretHeader, testE2ESecret)
	scriptsResp, serr := client.Do(scriptsReq)
	if serr != nil {
		t.Fatalf("GET /scripts: %v", serr)
	}
	defer scriptsResp.Body.Close()
	var scriptsList struct {
		Scripts []map[string]interface{} `json:"scripts"`
	}
	json.NewDecoder(scriptsResp.Body).Decode(&scriptsList)
	for _, s := range scriptsList.Scripts {
		if s["id"] == "proj_e2e01__looper" {
			t.Error("looper worker should be terminated and removed from scripts after timeout")
		}
	}

	// Deploy and invoke a normal function to prove the runtime is still alive.
	// If the looper killed the entire runtime, this will fail.
	body2 := map[string]interface{}{
		"id": "healthy", "name": "Healthy",
		"files": []map[string]string{
			{"path": testIndexTS, "content": `export default () => Response.json({ alive: true });`},
		},
	}
	w = doReq(r, "POST", testE2EFnPath, body2)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy healthy: %d, body=%s", w.Code, w.Body.String())
	}
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/healthy/invoke", map[string]string{})
	if w.Code != 200 {
		t.Fatalf("healthy function should work after looper killed: %d, body=%s", w.Code, w.Body.String())
	}
	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["alive"] != true {
		t.Errorf("expected alive=true, got %+v", result)
	}
}

func TestFnE2E_MetricsEndpoint(t *testing.T) {
	_, base, cleanup := setupFnE2E(t)
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}

	// GET /metrics should return Prometheus text format.
	req, _ := http.NewRequest("GET", base+"/metrics", nil)
	req.Header.Set(testRuntimeSecretHeader, testE2ESecret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /metrics: status %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Must contain at least these metric names (Prometheus text exposition).
	for _, metric := range []string{
		"excalibase_fn_invocations_total",
		"excalibase_fn_scripts_active",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("/metrics missing %q:\n%s", metric, body)
		}
	}
}

func TestFnE2E_ListAndDelete(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	// Deploy two functions
	for _, id := range []string{"fn-a", "fn-b"} {
		body := map[string]interface{}{
			"id": id, "name": id,
			"files": []map[string]string{
				{"path": testIndexTS, "content": `export default () => Response.json({ ok: 1 });`},
			},
		}
		w := doReq(r, "POST", testE2EFnPath, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("deploy %s: %d, body=%s", id, w.Code, w.Body.String())
		}
	}

	// List
	w := doReq(r, "GET", testE2EFnPath, nil)
	var list []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(list))
	}

	// Delete one
	w = doReq(r, "DELETE", "/api/projects/proj_e2e01/functions/fn-a", nil)
	if w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}

	// List again → one left
	w = doReq(r, "GET", testE2EFnPath, nil)
	var list2 []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list2)
	if len(list2) != 1 || list2[0].ID != "fn-b" {
		t.Errorf("after delete: %+v", list2)
	}

	// Invoking deleted function → 500 from runtime (function not found)
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/fn-a/invoke", map[string]string{})
	if w.Code == 200 {
		t.Error("invoking deleted function should not return 200")
	}
}
