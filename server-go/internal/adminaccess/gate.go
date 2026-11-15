package adminaccess

import (
	"net/http"
	"strings"
)

// AssertionHeader is where Cloudflare Access puts its signed assertion.
const AssertionHeader = "Cf-Access-Jwt-Assertion"

// Checker verifies an Access assertion; *Verifier is the real one.
type Checker interface {
	Verify(assertion string) error
}

// GateConfig says where the console lives and what proves a request came
// through Access.
type GateConfig struct {
	// Disabled switches the console off: every admin route but the
	// service-account routes answers 404 on every host. A cloud install with no
	// AdminHost runs this way, so it fails closed.
	Disabled bool
	// AdminHost is the console's hostname. Empty, with the console not
	// Disabled, switches the gate off (a self-hosted install).
	AdminHost string
	// Verifier checks the Access assertion. Nil leaves the host as the only
	// fence, which is what an install without Access has.
	Verifier Checker
	// IsInternal says whether the request came in on the in-cluster listener.
	// Its service-account routes keep their service address and skip this gate;
	// everything else, the console included, is fenced like any other request.
	IsInternal func(*http.Request) bool
}

// adminHostAPI is the API the console itself uses: its own routes, sign-in,
// the config and capacity reads Studio makes, and the two probes its guard waits on.
var adminHostAPI = []string{
	"/api/admin/", "/api/auth/", "/api/config", "/api/capacity",
	"/api/vault/status", "/healthz",
}

// otherServices are the data-plane and service surfaces that never belong on
// the console's hostname. Anything else outside /api is the Studio's own assets.
var otherServices = []string{"/functions/", "/storage/", "/internal/", "/metrics", "/s3"}

// Gate fences the console to its hostname and, behind it, to Cloudflare
// Access. On the admin host only the console's API answers, and only with a
// valid assertion; on every other host /api/admin looks unmounted. The
// platform_admin checks on each route stay on top of this.
func Gate(cfg GateConfig) func(http.Handler) http.Handler {
	adminHost := strings.ToLower(strings.TrimSpace(cfg.AdminHost))
	return func(next http.Handler) http.Handler {
		if cfg.Disabled {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isAdminAPI(r.URL.Path) && !isServiceAccountRoute(r.URL.Path) {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
		if adminHost == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.IsInternal != nil && cfg.IsInternal(r) && isServiceAccountRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if hostOnly(r.Host) != adminHost {
				if isAdminAPI(r.URL.Path) {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if cfg.Verifier != nil && cfg.Verifier.Verify(r.Header.Get(AssertionHeader)) != nil {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			if !servedOnAdminHost(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isServiceAccountRoute is the one admin route the bootstrap Job reaches over
// the in-cluster listener; it has its own gates and never meets the console.
func isServiceAccountRoute(path string) bool {
	return strings.HasPrefix(path, "/api/admin/service-accounts/") || path == "/api/admin/service-accounts"
}

func isAdminAPI(path string) bool {
	return path == "/api/admin" || strings.HasPrefix(path, "/api/admin/")
}

func servedOnAdminHost(path string) bool {
	if strings.HasPrefix(path, "/api/") || path == "/api" {
		for _, prefix := range adminHostAPI {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}
	for _, prefix := range otherServices {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}

// hostOnly lowercases a request host and drops its port.
func hostOnly(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}
