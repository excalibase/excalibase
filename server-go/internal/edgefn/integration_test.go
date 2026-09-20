//go:build integration

package edgefn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testDeployFmt = "deploy: %v"
	testAPIBase   = "https://api.test/"
	testInvokeFmt = "invoke: %v"
	testValueA    = "value-A"
	testValueB    = "value-B"
)

// Tests in this file spin up the real Deno runtime as a subprocess and talk to
// it over HTTP via the Go RuntimeClient. Run with:
//
//   go test -tags=integration ./internal/edgefn/ -run TestIntegration -v
//
// Requires:
//   - deno binary on PATH or at ~/.deno/bin/deno
//   - deno-server/server.ts present at repo root/../deno-server/server.ts
//   - a free TCP port (we pick one at runtime)

func findDenoBinary() string {
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

func findDenoServerTS() string {
	// server-go/internal/edgefn → ../../../deno-server/server.ts
	wd, _ := os.Getwd()
	candidate := filepath.Clean(filepath.Join(wd, "..", "..", "..", "deno-server", "server.ts"))
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// startRuntime spawns the Deno runtime on a free port and waits for /health
// to respond. Returns the base URL and a cleanup function.
func startRuntime(t *testing.T) (baseURL string, cleanup func()) {
	t.Helper()

	deno := findDenoBinary()
	if deno == "" {
		t.Skip("deno binary not found; skipping integration test")
	}
	serverTS := findDenoServerTS()
	if serverTS == "" {
		t.Skipf("deno-server/server.ts not found relative to wd=%s", mustWD(t))
	}

	// Pick a free port by binding :0 and immediately releasing.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	// Runtime reads PORT internally at 8000 by default. Our server.ts has
	// Deno.serve({ port: 8000 }) hardcoded — so we shim via a wrapper that
	// sets DENO_ENV and then we rewrite... actually simpler: pass PORT via env
	// and modify server.ts to honor it. But we don't want to touch server.ts
	// just for tests. Alternative: use the default 8000 and accept that only
	// one integration test can run at a time.
	//
	// Use port 8000 as-is. Serial execution is OK for this file.
	_ = port
	port = 8000

	secret := "integration-test-secret"
	cmd := exec.Command(deno, "run",
		"--allow-env", "--allow-net", "--allow-read",
		"--unstable-worker-options",
		serverTS,
	)
	cmd.Env = append(os.Environ(),
		"RUNTIME_SECRET="+secret,
	)
	// Capture stderr/stdout for debugging
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start deno: %v", err)
	}

	// Drain output so the subprocess doesn't block.
	go io.Copy(os.Stderr, stderr)
	go io.Copy(os.Stdout, stdout)

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	kill := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	// Wait for /health.
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
	t.Fatalf("deno runtime did not become healthy within deadline")
	return "", nil
}

func mustWD(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return wd
}

func newClient(base string) *RuntimeClient {
	return NewRuntimeClient(base, "integration-test-secret")
}

// --- End-to-end: deploy + invoke a real function ---

func TestIntegration_DeployAndInvoke_SingleFile(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	fn := &Function{
		ProjectID: "proj_inttest01",
		ID:        "hello",
		Name:      "Hello Integration",
		Files: []File{
			{Path: testIndexTS, Content: `export default async (req: Request): Promise<Response> => {
  const { name = "anon" } = await req.json().catch(() => ({}));
  return Response.json({ greeting: "hi " + name });
};`},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if err := client.Deploy(context.Background(), DeployRequest{
		ID: fn.RuntimeID(), Code: code, Secrets: map[string]string{},
	}); err != nil {
		t.Fatalf(testDeployFmt, err)
	}

	resp, err := client.Invoke(context.Background(), fn.RuntimeID(), InvokeRequest{
		Method: "POST", URL: testAPIBase, Headers: map[string]string{"Content-Type": "application/json"},
		Body: `{"name":"world"}`,
	})
	if err != nil {
		t.Fatalf(testInvokeFmt, err)
	}
	if resp.Status != 200 {
		t.Errorf("status: %d", resp.Status)
	}
	var body struct {
		Greeting string `json:"greeting"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("decode body: %v, raw=%q", err, resp.Body)
	}
	if body.Greeting != "hi world" {
		t.Errorf("greeting: %q", body.Greeting)
	}
}

func TestIntegration_DeployAndInvoke_MultiFile(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	fn := &Function{
		ProjectID: "proj_inttest02",
		ID:        "greet",
		Name:      "Greet",
		Files: []File{
			{Path: "utils.ts", Content: `export const shout = (msg: string) => msg.toUpperCase() + '!!'`},
			{Path: testIndexTS, Content: `import { shout } from './utils.ts'
export default (req: Request) => Response.json({ yell: shout('hello') })`},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if err := client.Deploy(context.Background(), DeployRequest{
		ID: fn.RuntimeID(), Code: code,
	}); err != nil {
		t.Fatalf("deploy: %v\ncode:\n%s", err, code)
	}

	resp, err := client.Invoke(context.Background(), fn.RuntimeID(), InvokeRequest{
		Method: "POST", URL: testAPIBase, Body: "{}",
	})
	if err != nil {
		t.Fatalf(testInvokeFmt, err)
	}
	if !strings.Contains(resp.Body, "HELLO!!") {
		t.Errorf("expected shouted 'HELLO!!' in body, got: %s", resp.Body)
	}
}

func TestIntegration_SecretsInjectedAsDenoEnv(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	fn := &Function{
		ProjectID: "proj_inttest03",
		ID:        "envread",
		Name:      "Env Read",
		Files: []File{
			{Path: testIndexTS, Content: `export default (req: Request) => {
  const stripeKey = Deno.env.get('STRIPE_KEY');
  const url = Deno.env.get('EXCALIBASE_URL');
  return Response.json({ stripeKey, url });
};`},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}

	secrets := map[string]string{
		"STRIPE_KEY":     "sk_test_supersecret",
		"EXCALIBASE_URL": "https://api.test/default/proj_inttest03",
	}
	if err := client.Deploy(context.Background(), DeployRequest{
		ID: fn.RuntimeID(), Code: code, Secrets: secrets,
	}); err != nil {
		t.Fatalf(testDeployFmt, err)
	}

	resp, err := client.Invoke(context.Background(), fn.RuntimeID(), InvokeRequest{
		Method: "POST", URL: testAPIBase, Body: "{}",
	})
	if err != nil {
		t.Fatalf(testInvokeFmt, err)
	}
	var body struct {
		StripeKey string `json:"stripeKey"`
		URL       string `json:"url"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("decode: %v raw=%q", err, resp.Body)
	}
	if body.StripeKey != "sk_test_supersecret" {
		t.Errorf("STRIPE_KEY: %q", body.StripeKey)
	}
	if body.URL != "https://api.test/default/proj_inttest03" {
		t.Errorf("EXCALIBASE_URL: %q", body.URL)
	}
}

func TestIntegration_SecretsAreIsolatedBetweenProjects(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	// Two functions with the same code but different projects and secrets.
	code := `export default (req: Request) => Response.json({ secret: Deno.env.get('THE_KEY') })`

	// Project A
	fnA := &Function{ProjectID: "proj_isolateA", ID: "h", Files: []File{{Path: testIndexTS, Content: "export default " + code[15:]}}}
	_ = fnA // use the more direct deploy below
	if err := client.Deploy(context.Background(), DeployRequest{
		ID:      "proj_isolateA__h",
		Code:    "globalThis.__excalibase_default = " + code[15:] + ";",
		Secrets: map[string]string{"THE_KEY": testValueA},
	}); err != nil {
		t.Fatalf("deploy A: %v", err)
	}
	if err := client.Deploy(context.Background(), DeployRequest{
		ID:      "proj_isolateB__h",
		Code:    "globalThis.__excalibase_default = " + code[15:] + ";",
		Secrets: map[string]string{"THE_KEY": testValueB},
	}); err != nil {
		t.Fatalf("deploy B: %v", err)
	}

	respA, err := client.Invoke(context.Background(), "proj_isolateA__h", InvokeRequest{Method: "POST", Body: "{}"})
	if err != nil {
		t.Fatalf("invoke A: %v", err)
	}
	respB, err := client.Invoke(context.Background(), "proj_isolateB__h", InvokeRequest{Method: "POST", Body: "{}"})
	if err != nil {
		t.Fatalf("invoke B: %v", err)
	}
	if !strings.Contains(respA.Body, testValueA) {
		t.Errorf("A should see value-A: %s", respA.Body)
	}
	if !strings.Contains(respB.Body, testValueB) {
		t.Errorf("B should see value-B: %s", respB.Body)
	}
	if strings.Contains(respA.Body, testValueB) || strings.Contains(respB.Body, testValueA) {
		t.Error("secrets leaked across projects")
	}
}

// Regression test for the worker.onmessage race: concurrent invocations on
// the same function must all complete with the right correlated response.
// Pre-fix, the second invoke's onmessage assignment overwrote the first's
// resolver and the first promise hung until INVOKE_TIMEOUT_MS.
func TestIntegration_ConcurrentInvokesNoRace(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	// Echo function — returns the request body so we can verify each call
	// sees its own input. Includes a small delay to maximize overlap.
	fn := &Function{
		ProjectID: "proj_concurrency",
		ID:        "echo",
		Name:      "Echo",
		Files: []File{
			{Path: testIndexTS, Content: `export default async (req: Request): Promise<Response> => {
  const { n } = await req.json();
  // small async pause so requests overlap inside the worker
  await new Promise(r => setTimeout(r, 20));
  return Response.json({ got: n });
};`},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if err := client.Deploy(context.Background(), DeployRequest{ID: fn.RuntimeID(), Code: code}); err != nil {
		t.Fatalf(testDeployFmt, err)
	}

	const N = 25
	type result struct {
		i  int
		ok bool
	}
	results := make(chan result, N)

	for i := 0; i < N; i++ {
		go func(i int) {
			body := fmt.Sprintf(`{"n":%d}`, i)
			resp, err := client.Invoke(context.Background(), fn.RuntimeID(), InvokeRequest{
				Method: "POST", URL: testAPIBase, Body: body,
			})
			if err != nil {
				results <- result{i, false}
				return
			}
			// Parse the body and verify it matches what we sent
			var r struct {
				Got int `json:"got"`
			}
			if err := json.Unmarshal([]byte(resp.Body), &r); err != nil {
				results <- result{i, false}
				return
			}
			results <- result{i, r.Got == i}
		}(i)
	}

	correct := 0
	failed := 0
	for k := 0; k < N; k++ {
		r := <-results
		if r.ok {
			correct++
		} else {
			failed++
		}
	}
	if failed > 0 {
		t.Errorf("concurrent invokes: %d/%d failed (race + correlation regression)", failed, N)
	}
	if correct != N {
		t.Errorf("expected all %d concurrent calls to succeed and see their own input, got %d correct", N, correct)
	}
}

func TestIntegration_ResponseStatusHeadersPassthrough(t *testing.T) {
	base, cleanup := startRuntime(t)
	defer cleanup()

	client := newClient(base)

	fn := &Function{
		ProjectID: "proj_passthru", ID: "status", Name: "Status",
		Files: []File{
			{Path: testIndexTS, Content: `export default (req: Request) => new Response('nope', {
  status: 418,
  headers: { 'X-Excalibase-Teapot': 'yes', 'Content-Type': 'text/plain' },
});`},
		},
	}
	code, _ := fn.Bundle()
	if err := client.Deploy(context.Background(), DeployRequest{ID: fn.RuntimeID(), Code: code}); err != nil {
		t.Fatalf(testDeployFmt, err)
	}

	resp, err := client.Invoke(context.Background(), fn.RuntimeID(), InvokeRequest{Method: "GET"})
	if err != nil {
		t.Fatalf(testInvokeFmt, err)
	}
	if resp.Status != 418 {
		t.Errorf("status: %d, want 418", resp.Status)
	}
	if resp.Headers["x-excalibase-teapot"] != "yes" && resp.Headers["X-Excalibase-Teapot"] != "yes" {
		t.Errorf("custom header not passed through: %+v", resp.Headers)
	}
	if resp.Body != "nope" {
		t.Errorf("body: %q", resp.Body)
	}
}
