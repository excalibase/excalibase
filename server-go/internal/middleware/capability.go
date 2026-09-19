package middleware

import (
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// Capability tokens (EXC-365) are the credentials the platform's own services
// authenticate with. Unlike a human PAT they are default-deny: the table below
// is the complete list of requests a capability token may make, and everything
// else — every write, every route not named here — is refused with 403 whatever
// the owning service principal's platform role would otherwise permit.
//
// Adding a route here widens what every existing service token can reach, so
// each entry names the narrowest capability that makes the call work.
const (
	vaultSecretPrefix = "/api/vault/secrets/"
	selfRoute         = "/api/auth/me"
	// emailRelayRoute is POST /internal/email/send — the server-to-server
	// mail relay the auth service calls; it has no mail SDK of its own.
	emailRelayRoute = "/internal/email/send"

	capResourceVault    = "vault"
	capResourcePolicies = "policies"
	capResourceProjects = "projects"
	capResourceEmail    = "email"
	capActionRead       = "read"
	capActionInfo       = "info"
	capActionSend       = "send"

	errBodyCapability = `{"error":"token is not permitted to call this endpoint"}`
)

var (
	// projectInfoRoute is GET /api/projects/{projectId}/info — the engine's
	// per-project connection details and CORS origins.
	projectInfoRoute = regexp.MustCompile(`^/api/projects/[^/]+/info$`)
	// policyRoute is the RLS and column-policy read surface the engine polls.
	policyRoute = regexp.MustCompile(`^/api/provision/[^/]+/(rls-policies|column-policies)(/[^/]+)?$`)
)

// RequiredCapability returns the capability a request must be granted before a
// capability token may make it. ok=false means no capability can authorize the
// request — the gate refuses it outright.
func RequiredCapability(method, rawPath string) (auth.Capability, bool) {
	cleaned := normalizePath(rawPath)
	// The one write a capability token may make: relaying a transactional
	// email. Checked before the read-only guard below, which every other
	// route is still held to.
	if method == http.MethodPost && cleaned == emailRelayRoute {
		return EmailRelayCapability(), true
	}
	if method != http.MethodGet && method != http.MethodHead {
		return auth.Capability{}, false
	}
	if secret, ok := strings.CutPrefix(cleaned, vaultSecretPrefix); ok {
		if secret == "" {
			return auth.Capability{}, false
		}
		return auth.Capability{Resource: capResourceVault, Action: capActionRead, Selector: secret}, true
	}
	if projectInfoRoute.MatchString(cleaned) {
		return auth.Capability{Resource: capResourceProjects, Action: capActionInfo, Selector: capActionRead}, true
	}
	if policyRoute.MatchString(cleaned) {
		return auth.Capability{Resource: capResourcePolicies, Action: capActionRead}, true
	}
	return auth.Capability{}, false
}

// EmailRelayCapability is the capability POST /internal/email/send demands.
func EmailRelayCapability() auth.Capability {
	return auth.Capability{Resource: capResourceEmail, Action: capActionSend}
}

// normalizePath resolves "." and ".." the way the router will, so the gate can
// never be handed a path that decodes to a different route than it inspected.
// A trailing slash is dropped; path.Clean already does the rest.
func normalizePath(rawPath string) string {
	if rawPath == "" {
		return rawPath
	}
	return path.Clean(rawPath)
}

// CapabilityGate refuses any request a capability token is not permitted to
// make. Requests authenticated by an ordinary PAT, a session, or no token at
// all pass straight through — this layer adds no gate for them; the existing
// role, scope and project-access middleware remains their only authority.
//
// Mount it on the root router directly after auth.ExtractAuth so it sees every
// route, including ones added later that nobody remembered to consider.
func CapabilityGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := auth.GetToken(r.Context())
		if !auth.IsCapabilityToken(token) {
			next.ServeHTTP(w, r)
			return
		}
		if normalizePath(r.URL.Path) == selfRoute {
			next.ServeHTTP(w, r)
			return
		}
		want, ok := RequiredCapability(r.Method, r.URL.Path)
		if !ok || !auth.TokenGrants(token, want) {
			http.Error(w, errBodyCapability, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCapability is the converse of CapabilityGate: mount it on a route
// that exists only for a service principal and every other caller is refused.
// The gate above bounds what a capability token may reach but lets human PATs
// and sessions through untouched, so without this a logged-in studio user
// would reach a service-only route. Authentication itself stays with
// auth.RequireAuth, which answers 401 above this layer.
func RequireCapability(want auth.Capability) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.TokenGrants(auth.GetToken(r.Context()), want) {
				http.Error(w, errBodyCapability, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
