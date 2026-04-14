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
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
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
	secret := "handler-e2e-secret"
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
		req.Header.Set("X-Runtime-Secret", secret)
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

func (s *e2eInstanceStore) Save(inst *domain.DatabaseInstance) error {
	s.insts[inst.ProjectID] = inst
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
func (f *e2eFakeVault) Sealed() bool                         { return false }
func (f *e2eFakeVault) GetPublicKey() (string, error)        { return "", nil }

func setupFnE2E(t *testing.T) (*chi.Mux, string, func()) {
	t.Helper()
	base, cleanup := startDenoRuntime(t)

	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	vault := &e2eFakeVault{data: map[string]map[string]string{}}
	secrets := edgefn.NewSecretsStore(vault)
	client := edgefn.NewRuntimeClient(base, "handler-e2e-secret")

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
			{"path": "index.ts", "content": `export default async (req: Request): Promise<Response> => {
  const { name = 'anon' } = await req.json().catch(() => ({}));
  return Response.json({ hello: name });
};`},
		},
	}
	w := doReq(r, "POST", "/api/projects/proj_e2e01/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d, body=%s", w.Code, w.Body.String())
	}

	// Invoke
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/greet/invoke", map[string]string{"name": "world"})
	if w.Code != 200 {
		t.Fatalf("invoke: %d, body=%s", w.Code, w.Body.String())
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
	w := doReq(r, "POST", "/api/projects/proj_e2e01/functions/secrets",
		map[string]string{"key": "API_TOKEN", "value": "tok_sekrit"})
	if w.Code != 200 {
		t.Fatalf("set secret: %d, body=%s", w.Code, w.Body.String())
	}

	// Deploy a function that reads the secret
	body := map[string]interface{}{
		"id":   "read-env",
		"name": "Read Env",
		"files": []map[string]string{
			{"path": "index.ts", "content": `export default (req: Request) => Response.json({
  token: Deno.env.get('API_TOKEN'),
  url: Deno.env.get('EXCALIBASE_URL'),
  pid: Deno.env.get('EXCALIBASE_PROJECT_ID'),
});`},
		},
	}
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d, body=%s", w.Code, w.Body.String())
	}

	// Invoke → function should see the secret + built-ins
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/read-env/invoke", map[string]string{})
	if w.Code != 200 {
		t.Fatalf("invoke: %d, body=%s", w.Code, w.Body.String())
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
			{"path": "index.ts", "content": `export default (req: Request) => Response.json({ v: Deno.env.get('ROTATE_ME') || 'unset' });`},
		},
	}
	w := doReq(r, "POST", "/api/projects/proj_e2e01/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d", w.Code)
	}

	// Invoke → should be 'unset'
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/echo-key/invoke", map[string]string{})
	var r1 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r1)
	if r1["v"] != "unset" {
		t.Errorf("pre-set: %q, want 'unset'", r1["v"])
	}

	// Set secret — handler redeploys automatically
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/secrets",
		map[string]string{"key": "ROTATE_ME", "value": "first-value"})
	if w.Code != 200 {
		t.Fatalf("set secret: %d, body=%s", w.Code, w.Body.String())
	}

	// Invoke again → should be 'first-value'
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/echo-key/invoke", map[string]string{})
	var r2 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r2)
	if r2["v"] != "first-value" {
		t.Errorf("after set: %q, want 'first-value'", r2["v"])
	}

	// Rotate the value
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/secrets",
		map[string]string{"key": "ROTATE_ME", "value": "second-value"})
	if w.Code != 200 {
		t.Fatalf("rotate secret: %d", w.Code)
	}
	w = doReq(r, "POST", "/api/projects/proj_e2e01/functions/echo-key/invoke", map[string]string{})
	var r3 map[string]string
	json.Unmarshal(w.Body.Bytes(), &r3)
	if r3["v"] != "second-value" {
		t.Errorf("after rotate: %q, want 'second-value'", r3["v"])
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
				{"path": "index.ts", "content": `export default () => Response.json({ ok: 1 });`},
			},
		}
		w := doReq(r, "POST", "/api/projects/proj_e2e01/functions/", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("deploy %s: %d, body=%s", id, w.Code, w.Body.String())
		}
	}

	// List
	w := doReq(r, "GET", "/api/projects/proj_e2e01/functions/", nil)
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
	w = doReq(r, "GET", "/api/projects/proj_e2e01/functions/", nil)
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
