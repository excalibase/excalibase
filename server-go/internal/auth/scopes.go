package auth

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Token scopes. A token carries a comma-separated set; empty means a legacy
// all-purpose token created before the scopes column existed.
const (
	// ScopeSession is stamped on the token minted by /login; it is never
	// accepted from a token-creation request.
	ScopeSession = "session"
	// ScopeRead allows safe methods (GET/HEAD) only.
	ScopeRead = "read"
	// ScopeWrite allows mutating methods on project-scoped routes.
	ScopeWrite = "write"
	// ScopeAdmin is a superset of write kept for pre-existing tokens.
	ScopeAdmin = "admin"
)

// creatableScopes are the scopes a caller may request on a new PAT.
var creatableScopes = map[string]bool{ScopeRead: true, ScopeWrite: true, ScopeAdmin: true}

// writeScopes are the scopes that permit a mutating request.
var writeScopes = []string{ScopeSession, ScopeWrite, ScopeAdmin}

// NormalizeScopes validates a requested scope list and returns it as the
// canonical comma-separated form (trimmed, deduplicated, sorted). An empty
// list yields "" — a legacy all-purpose token.
func NormalizeScopes(requested []string) (string, error) {
	seen := map[string]bool{}
	for _, raw := range requested {
		scope := strings.TrimSpace(raw)
		if !creatableScopes[scope] {
			return "", fmt.Errorf("unknown scope: %q", scope)
		}
		seen[scope] = true
	}
	out := make([]string, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

// TokenBoundToProject reports whether the token may act on projectID. A nil
// or unbound token is not restricted; a bound token only reaches its own
// project.
func TokenBoundToProject(t *domain.AccessToken, projectID string) bool {
	if t == nil || t.ProjectID == "" {
		return true
	}
	return t.ProjectID == projectID
}

// TokenAllowsMethod reports whether the token's scopes permit the HTTP
// method. Safe methods are always allowed; mutating ones need a write-capable
// scope. A nil token or legacy empty scopes pass (see RequireScope).
func TokenAllowsMethod(t *domain.AccessToken, method string) bool {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return true
	}
	if t == nil || t.Scopes == "" {
		return true
	}
	for _, scope := range writeScopes {
		if TokenHasScope(t, scope) {
			return true
		}
	}
	return false
}
