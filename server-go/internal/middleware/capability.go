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
// else — every route not named here — is refused with 403 whatever the owning
// service principal's platform role would otherwise permit. The writes named
// here are the email relay, and the vault init/unseal and service-token
// management calls of the svc-bootstrap principal (EXC-485).
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

	vaultInitRoute       = "/api/vault/init"
	vaultUnsealRoute     = "/api/vault/unseal"
	serviceAccountsRoute = "/api/admin/service-accounts"
	tokensRoute          = "/api/auth/tokens"
)

var (
	// projectInfoRoute is GET /api/projects/{projectId}/info — the engine's
	// per-project connection details and CORS origins.
	projectInfoRoute = regexp.MustCompile(`^/api/projects/[^/]+/info$`)
	// serviceAccountTokensRoute is GET /api/admin/service-accounts/{name}/tokens.
	serviceAccountTokensRoute = regexp.MustCompile(`^/api/admin/service-accounts/([^/]+)/tokens$`)
	// tokenByHashRoute and tokenRotateRoute name one token by its hash.
	tokenByHashRoute = regexp.MustCompile(`^/api/auth/tokens/[^/]+$`)
	tokenRotateRoute = regexp.MustCompile(`^/api/auth/tokens/[^/]+/rotate$`)
	// policyRoute is the policy read surface the engine polls. table-grants
	// (EXC-370) is part of it: the engine fetches the exposure list in the
	// same round as the two policy sets and enforces them together, so a
	// grant that covers one without the other leaves the engine with half a
	// policy set and no way to answer a query.
	policyRoute = regexp.MustCompile(`^/api/provision/[^/]+/(rls-policies|column-policies|table-grants)(/[^/]+)?$`)
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
	// The vault lifecycle calls the bootstrap Job makes (EXC-485).
	if method == http.MethodPost && cleaned == vaultInitRoute {
		return auth.VaultInitCapability(), true
	}
	if method == http.MethodPost && cleaned == vaultUnsealRoute {
		return auth.VaultUnsealCapability(), true
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
	if m := serviceAccountTokensRoute.FindStringSubmatch(cleaned); m != nil {
		return auth.ManageServiceTokensCapability(m[1]), true
	}
	return auth.Capability{}, false
}

// RequiresServiceTokenManager reports whether the request is one of the token
// management calls whose subject is named in the body or by a token hash:
// creating a service principal, minting a token, revoking or rotating one.
// The gate admits them for a token that manages any service principal's
// tokens; the handler then binds the subject to one the token names.
func RequiresServiceTokenManager(method, rawPath string) bool {
	cleaned := normalizePath(rawPath)
	switch method {
	case http.MethodPost:
		return cleaned == serviceAccountsRoute || cleaned == tokensRoute || tokenRotateRoute.MatchString(cleaned)
	case http.MethodDelete:
		return tokenByHashRoute.MatchString(cleaned)
	}
	return false
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
		// This gate reads the decoded r.URL.Path, but chi routes on the
		// escaped r.URL.RawPath and hands the vault handler the still-escaped
		// wildcard — so an escaped path would be authorized as one secret and
		// served as another. No path a service legitimately reads needs an
		// escape, so refuse the whole class instead of picking a form.
		if r.URL.RawPath != "" {
			http.Error(w, errBodyCapability, http.StatusForbidden)
			return
		}
		if normalizePath(r.URL.Path) == selfRoute {
			next.ServeHTTP(w, r)
			return
		}
		if RequiresServiceTokenManager(r.Method, r.URL.Path) && auth.TokenManagesServiceTokens(token) {
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

// UnlessGrantedCapability wraps a guard so that a capability token whose list
// grants the capability this request needs passes without it. Mounted where a
// route family is otherwise reserved for unrestricted human credentials but
// one or two calls in it belong to a service (the vault lifecycle calls the
// bootstrap Job makes, EXC-485). Every other caller still meets the guard.
func UnlessGrantedCapability(guard func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := guard(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := auth.GetToken(r.Context())
			if auth.IsCapabilityToken(token) {
				if want, ok := RequiredCapability(r.Method, r.URL.Path); ok && auth.TokenGrants(token, want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			guarded.ServeHTTP(w, r)
		})
	}
}
