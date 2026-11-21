package handler

import (
	"io"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// Functions answer on the platform's shared API origin, so what they may set on
// that origin is narrowed here (EXC-808): the runtime hands back every header
// the function wrote, and the gateway is the place that decides which survive.

// hopByHopHeaders belong to the runtime's connection, not the caller's.
var hopByHopHeaders = map[string]bool{"transfer-encoding": true, "connection": true, "content-length": true}

// platformOwnedHeaders are set by the platform for the whole origin; a function
// cannot set, replace or clear them. Clear-Site-Data, Report-To, NEL and
// Reporting-Endpoints would act on every tenant of the origin.
var platformOwnedHeaders = map[string]bool{
	"content-security-policy":             true, // also overwritten below; listed so the strip is explicit
	"content-security-policy-report-only": true,
	"x-frame-options":                     true,
	"strict-transport-security":           true,
	"permissions-policy":                  true,
	"referrer-policy":                     true,
	"clear-site-data":                     true,
	"report-to":                           true,
	"reporting-endpoints":                 true,
	"nel":                                 true,
	"service-worker-allowed":              true,
	"set-cookie2":                         true,
}

// platformCookieNames are the cookies the platform sets in the browser, with
// and without the __Host- prefix (plain-http installs drop it).
var platformCookieNames = map[string]bool{
	auth.LegacySessionCookieName: true,
	oauthStateCookie:             true,
	siteGitBrowserCookie:         true,
}

// cookieAttributesKept are the attributes of a function's cookie that stay as
// the function wrote them; Domain and Path are never among them.
var cookieAttributesKept = map[string]bool{
	"expires": true, "max-age": true, "secure": true, "httponly": true, "samesite": true, "partitioned": true,
}

// functionCookiePath is the only path a function's cookie may carry: its
// project's own function URLs, so the browser never sends it elsewhere on the
// shared origin and it cannot shadow another tenant's cookie.
func functionCookiePath(projectID string) string {
	return "/functions/v1/" + projectID + "/"
}

// sanitizeFunctionCookie returns the Set-Cookie value to relay, or false when
// the cookie must be dropped: no usable name, a Domain attribute, or a name the
// platform owns or protects with a __Host-/__Secure- prefix.
func sanitizeFunctionCookie(raw, projectID string) (string, bool) {
	parts := strings.Split(raw, ";")
	name, value, found := strings.Cut(strings.TrimSpace(parts[0]), "=")
	name = strings.TrimSpace(name)
	if !found || !validCookieToken(name) || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "__host-") || strings.HasPrefix(lower, "__secure-") || platformCookieNames[lower] {
		return "", false
	}
	var out strings.Builder
	out.WriteString(name + "=" + strings.TrimSpace(value))
	for _, attr := range parts[1:] {
		key, _, _ := strings.Cut(strings.TrimSpace(attr), "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "domain" {
			return "", false
		}
		if cookieAttributesKept[key] {
			out.WriteString("; " + strings.TrimSpace(attr))
		}
	}
	out.WriteString("; Path=" + functionCookiePath(projectID))
	return out.String(), true
}

func validCookieToken(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={}`, r) {
			return false
		}
	}
	return true
}

// writeRuntimeResponse forwards a runtime InvokeResponse back to the caller
// with the handler's original status and body. Headers pass through except
// those that would let a function act on the shared origin: see
// platformOwnedHeaders, sanitizeFunctionCookie and the sandbox policy below.
func writeRuntimeResponse(w http.ResponseWriter, projectID string, resp *edgefn.InvokeResponse) {
	for k, v := range resp.Headers {
		lk := strings.ToLower(k)
		switch {
		case hopByHopHeaders[lk], platformOwnedHeaders[lk], strings.HasPrefix(lk, "cross-origin-"):
		case lk == "set-cookie":
			if cookie, ok := sanitizeFunctionCookie(v, projectID); ok {
				w.Header().Add("Set-Cookie", cookie)
			}
		default:
			w.Header().Set(k, v)
		}
	}
	// Set last so the function cannot clear it: Studio tells a function's own
	// 401 from its session ending by this mark.
	w.Header().Set(custommw.FunctionResponseHeader, "1")
	// Whatever HTML a function serves runs in an opaque origin.
	w.Header().Set("Content-Security-Policy", custommw.FunctionContentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	status := resp.Status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp.Body)
}

// refuseServiceWorkerScript answers 404 to a service-worker script fetch (the
// browser marks it Service-Worker: script, both on register and on its update
// checks). Function pages are sandboxed and cannot register workers now, and a
// 404 on an update check makes the browser unregister a worker an unsandboxed
// function page registered earlier on the shared origin.
func refuseServiceWorkerScript(w http.ResponseWriter, r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Service-Worker"), "script") {
		return false
	}
	httpError(w, errFunctionNotFound, http.StatusNotFound)
	return true
}
