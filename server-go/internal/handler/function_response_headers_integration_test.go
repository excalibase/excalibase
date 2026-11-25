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
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, an HTML answer is served as plain text (EXC-810)", got)
	}

	own := `export default () => new Response("x", { headers: { "set-cookie": "theme=dark; Domain=excalibase.io" }});`
	if w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, "dom", own)); w.Code != http.StatusCreated {
		t.Fatalf(testDeployFmt, w.Code, w.Body.String())
	}
	if got := doReq(r, "POST", testE2EFnPath+"dom/invoke", map[string]string{}).Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("a Domain cookie reached the caller: %v", got)
	}
}

// EXC-810: through the real Deno runtime, no way of writing the answer gets a
// page type to the caller.
func TestFnE2E_FunctionCannotServeAPageOnTheSharedOrigin(t *testing.T) {
	r, _, cleanup := setupFnE2E(t)
	defer cleanup()

	bodies := map[string]string{
		"html":      `new Response("<form>login</form>", { headers: { "content-type": "Text/HTML; charset=utf-8" } })`,
		"svg":       `new Response("<svg xmlns='http://www.w3.org/2000/svg'/>", { headers: { "content-type": "image/svg+xml" } })`,
		"xhtml":     `new Response("<html xmlns='http://www.w3.org/1999/xhtml'/>", { headers: { "content-type": "application/xhtml+xml" } })`,
		"twotypes":  `new Response("<p>x</p>", { headers: [["content-type", "text/plain"], ["Content-Type", "text/html"]] })`,
		"sniffed":   `new Response(new Uint8Array(new TextEncoder().encode("<html><body>login</body></html>")))`,
		"streamed":  `new Response(new ReadableStream({ start(c) { c.enqueue(new TextEncoder().encode("<html>")); c.enqueue(new TextEncoder().encode("login</html>")); c.close(); } }), { headers: { "content-type": "text/html" } })`,
		"redirect":  `new Response("<html>moved</html>", { status: 302, headers: { "content-type": "text/html", location: "https://example.test/" } })`,
		"nosniffed": `new Response("<html>x</html>", { headers: { "content-type": "text/html", "x-content-type-options": "" } })`,
	}
	for name, response := range bodies {
		src := "export default () => " + response + ";"
		if w := doReq(r, "POST", testE2EFnPath, deployContractFn(t, name, src)); w.Code != http.StatusCreated {
			t.Fatalf(testDeployFmt, w.Code, w.Body.String())
		}
		for _, method := range []string{"POST"} { // GET and HEAD are covered against a real server in the unit tests
			w := doReq(r, method, testE2EFnPath+name+"/invoke", map[string]string{})
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
				t.Errorf("%s %s: Content-Type = %q, want text/plain", name, method, got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s %s: X-Content-Type-Options = %q", name, method, got)
			}
		}
	}
}
