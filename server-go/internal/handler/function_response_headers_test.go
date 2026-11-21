package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

const testFnProject = "proj_abc"

func relay(headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Security-Policy", "global-policy")
	writeRuntimeResponse(rec, testFnProject, &edgefn.InvokeResponse{Status: 200, Headers: headers, Body: "ok"})
	return rec
}

func TestRuntimeResponseDropsCookiesWithADomain(t *testing.T) {
	for _, cookie := range []string{
		"sid=1; Domain=excalibase.io; Path=/",
		"sid=1; path=/; DOMAIN=.excalibase.io",
		"sid=1; domain=a.b",
	} {
		rec := relay(map[string]string{"set-cookie": cookie})
		if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("%q reached the caller as %v", cookie, got)
		}
	}
}

func TestRuntimeResponseDropsStudioAndPrefixedCookieNames(t *testing.T) {
	for _, name := range []string{
		"excali_session", "EXCALI_SESSION", "__Host-excali_session", "__Host-anything", "__host-x", "__Secure-x", "__SECURE-x",
	} {
		rec := relay(map[string]string{"set-cookie": name + "=v; Path=/; Secure"})
		if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("cookie %q reached the caller as %v", name, got)
		}
	}
}

func TestRuntimeResponsePinsCookiePathToTheProjectsFunctions(t *testing.T) {
	rec := relay(map[string]string{"set-cookie": "theme=dark; Path=/api; Max-Age=60; HttpOnly; SameSite=Lax; Priority=High"})
	got := rec.Header().Values("Set-Cookie")
	if len(got) != 1 {
		t.Fatalf("Set-Cookie = %v, want the function's own cookie kept", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "theme" || cookies[0].Value != "dark" {
		t.Fatalf("cookie = %+v", cookies)
	}
	c := cookies[0]
	if c.Path != "/functions/v1/proj_abc/" || c.Domain != "" || !c.HttpOnly || c.MaxAge != 60 || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie attributes = %+v, want path pinned, no domain, other attributes kept", c)
	}
	if strings.Contains(got[0], "/api") || strings.Contains(got[0], "Priority") {
		t.Errorf("function-chosen attributes survived: %q", got[0])
	}
}

func TestRuntimeResponsePinsPathWhenTheFunctionGivesNone(t *testing.T) {
	cookies := relay(map[string]string{"set-cookie": "a=b"}).Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/functions/v1/proj_abc/" {
		t.Fatalf("cookies = %+v", cookies)
	}
}

func TestRuntimeResponseDropsAnUnparsableCookie(t *testing.T) {
	for _, bad := range []string{"", "novalue", "=v", "a b=c", "a=b\r\nX: y"} {
		if got := relay(map[string]string{"set-cookie": bad}).Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("%q reached the caller as %v", bad, got)
		}
	}
}

func TestRuntimeResponseStripsSecurityPolicyHeaders(t *testing.T) {
	rec := relay(map[string]string{
		"content-security-policy":             "default-src *",
		"Content-Security-Policy-Report-Only": "x",
		"x-frame-options":                     "ALLOWALL",
		"strict-transport-security":           "max-age=0",
		"cross-origin-opener-policy":          "unsafe-none",
		"Cross-Origin-Embedder-Policy":        "unsafe-none",
		"cross-origin-resource-policy":        "cross-origin",
		"permissions-policy":                  "camera=*",
		"clear-site-data":                     `"cookies"`,
	})
	for _, name := range []string{
		"Content-Security-Policy-Report-Only", "X-Frame-Options", "Strict-Transport-Security",
		"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Cross-Origin-Resource-Policy",
		"Permissions-Policy", "Clear-Site-Data",
	} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("%s = %q, want stripped", name, got)
		}
	}
}

func TestRuntimeResponseAlwaysCarriesTheSandboxPolicy(t *testing.T) {
	rec := relay(map[string]string{"content-security-policy": "default-src *; script-src 'unsafe-inline'"})
	csp := rec.Header().Get("Content-Security-Policy")
	if csp != custommw.FunctionContentSecurityPolicy {
		t.Fatalf("CSP = %q, want the platform's sandbox policy", csp)
	}
	if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP %q must sandbox without allow-same-origin", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := relay(nil).Header().Get("Content-Security-Policy"); got != custommw.FunctionContentSecurityPolicy {
		t.Errorf("a function that sends no policy got %q", got)
	}
}

func TestRuntimeResponseLeavesAnOrdinaryJSONAnswerAlone(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRuntimeResponse(rec, testFnProject, &edgefn.InvokeResponse{
		Status:  201,
		Headers: map[string]string{"content-type": "application/json", "x-request-id": "r1", "cache-control": "no-store", "content-length": "9", "connection": "close"},
		Body:    `{"a":1}`,
	})
	if rec.Code != 201 || rec.Body.String() != `{"a":1}` {
		t.Fatalf("answer = %d %q", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{"Content-Type": "application/json", "X-Request-Id": "r1", "Cache-Control": "no-store"} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if rec.Header().Get("Connection") != "" || rec.Header().Get("Content-Length") != "" {
		t.Error("hop-by-hop headers were forwarded")
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("unexpected cookie %v", got)
	}
}
