//go:build integration

package handler

import (
	"net/http"
	"strings"
	"testing"

	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// EXC-808: against the real Deno runtime, what a function writes into its
// headers cannot act on the shared API origin.
func TestFnE2E_FunctionCannotSetCookiesOrPoliciesOnTheSharedOrigin(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	src := `export default () => new Response("<h1>hi</h1>", { headers: {
		"content-type": "text/html",
		"content-security-policy": "default-src *",
		"x-frame-options": "ALLOWALL",
		"set-cookie": "__Host-excali_session=evil; Path=/; Secure",
	}});`
	if w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, "page", src)); w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}
	w := doReq(r, "POST", testE2EFnPath+"page/invoke", map[string]string{})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hi") {
		t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Security-Policy"); got != custommw.FunctionContentSecurityPolicy {
		t.Errorf("CSP = %q, want the sandbox policy", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want stripped", got)
	}
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("a __Host- session cookie reached the caller: %v", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, an ordinary header must survive", got)
	}

	own := `export default () => new Response("x", { headers: { "set-cookie": "theme=dark; Domain=excalibase.io" }});`
	if w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, "dom", own)); w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}
	if got := doReq(r, "POST", testE2EFnPath+"dom/invoke", map[string]string{}).Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("a Domain cookie reached the caller: %v", got)
	}
}
