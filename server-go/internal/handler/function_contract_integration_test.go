//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

// EXC-640: against the real Deno runtime, a function without a default
// handler is refused at deploy with the contract named, and what goes wrong
// is readable from the function's logs.

func deployContractFn(t *testing.T, id, content string) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"id": id, "name": id,
		"files": []map[string]string{{"path": testIndexTS, "content": content}},
	}
}

func fnLogText(t *testing.T, r chi.Router, id string) string {
	t.Helper()
	w := doReq(r, "GET", testE2EFnPath+id+"/logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Logs []edgefn.LogEntry `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	lines := make([]string, 0, len(out.Logs))
	for _, entry := range out.Logs {
		lines = append(lines, entry.Msg)
	}
	return strings.Join(lines, "\n")
}

func TestFnE2E_NamedExportIsRefusedAtDeployNamingTheContract(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, "invite",
		`export async function invite(req: Request): Promise<Response> { return new Response("x"); }`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("deploy: %d %s, want 400", w.Code, w.Body.String())
	}
	for _, want := range []string{"export default", "invite"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("deploy refusal %s does not mention %q", w.Body.String(), want)
		}
	}
	if w := doReq(r, "GET", testE2EFnPath+"invite", nil); w.Code != http.StatusNotFound {
		t.Errorf("a refused first deploy left a function behind: %d", w.Code)
	}
}

func TestFnE2E_HandlerCrashIsInTheFunctionLogs(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	if w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, "crash",
		`export default () => { throw new Error("orders table missing"); };`)); w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}
	w := doReq(r, "POST", testE2EFnPath+"crash/invoke", map[string]string{})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "orders table") {
		t.Fatalf("invoke: %d %s, want a bare 500", w.Code, w.Body.String())
	}
	time.Sleep(250 * time.Millisecond)
	if logs := fnLogText(t, r, "crash"); !strings.Contains(logs, "orders table missing") {
		t.Errorf("the crash is not in the function logs: %q", logs)
	}
}
