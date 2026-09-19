package routepolicy

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// Expectation is the outcome a row implies for one principal on one method.
type Expectation string

const (
	// Allow means the request must reach its handler — the gates must not
	// refuse it. What the handler then answers is not this table's business.
	Allow Expectation = "allow"
	// Deny401 is "no usable credential was presented".
	Deny401 Expectation = "401"
	// Deny403 is "the credential is understood and is not permitted".
	Deny403 Expectation = "403"
	// Deny404 is "this tenant resource must not be confirmed to exist".
	Deny404 Expectation = "404"
	// Unasserted marks a cell this matrix deliberately does not drive —
	// the route authenticates against a credential it does not mint. The
	// coverage test reports these rather than asserting them.
	Unasserted Expectation = "unasserted"
)

// Principal is a synthetic caller the matrix drives the real router with.
type Principal struct {
	Name string
	// Anonymous carries no credential at all.
	Anonymous bool
	// OrgRole is the caller's role in the org that owns the tenant named in
	// the path; "" means they are not a member of it.
	OrgRole string
	// PlatformAdmin holds every platform permission and bypasses org roles.
	PlatformAdmin bool
	// ReadOnly is a token whose scopes permit no mutating method.
	ReadOnly bool
	// Restricted is any narrowed PAT — scope-limited or project-bound.
	Restricted bool
	// ForeignProject is a PAT bound to a project other than the one in the
	// path, which may never leave its binding.
	ForeignProject bool
	// Capabilities is the permission list of a platform service token. A
	// non-empty list makes this a capability token, which is default-deny.
	Capabilities []string
}

// IsCapabilityToken reports whether the principal authenticates with a
// service capability token rather than a session or human PAT.
func (p Principal) IsCapabilityToken() bool { return len(p.Capabilities) > 0 }

// Expect returns the outcome the row implies for this principal and method.
func Expect(row Row, method string, p Principal) Expectation {
	// Preflight never reaches a route: the CORS middleware answers every
	// OPTIONS at the root, above authentication.
	if method == http.MethodOptions {
		return Unasserted
	}
	if p.IsCapabilityToken() {
		return expectCapabilityToken(row)
	}
	switch row.Auth {
	case AuthPublic:
		return Allow
	case AuthFunctionJWT:
		return Unasserted
	case AuthRuntimeToken:
		// None of these principals holds the project's derived runtime
		// secret, so every one of them is refused as unauthenticated.
		return Deny401
	}
	return expectSession(row, method, p)
}

// expectCapabilityToken applies the default-deny capability gate. A route the
// capability table does not name is refused whatever the owning service
// principal's platform role would otherwise permit. The allow direction needs
// the concrete selector of the request (a vault secret path, say), so it is
// left to the service-token contract test rather than asserted from a pattern.
func expectCapabilityToken(row Row) Expectation {
	if row.Capability == "" {
		return Deny403
	}
	return Unasserted
}

// expectSession applies the gates a session or human PAT meets, in the order
// the router runs them.
func expectSession(row Row, method string, p Principal) Expectation {
	if p.Anonymous {
		return Deny401
	}
	if row.Auth == AuthHandlerSession {
		// No auth.RequireAuth above the handler, so no scope or role gate
		// beyond "someone is logged in".
		return Allow
	}
	if row.ServiceOnly {
		return Deny403
	}
	if row.Param == ParamProject && p.ForeignProject {
		return Deny404
	}
	if isWrite(method) && p.ReadOnly {
		return Deny403
	}
	if row.Unrestricted && p.Restricted {
		return Deny403
	}
	if p.PlatformAdmin {
		return Allow
	}
	if row.Permission != "" {
		return Deny403
	}
	return expectTenant(row, p)
}

// expectTenant applies the org-role rung once the caller has cleared the
// token-level gates.
func expectTenant(row Row, p Principal) Expectation {
	switch row.Param {
	case ParamProject:
		if p.OrgRole == "" {
			return Deny404
		}
	case ParamOrg:
		if p.OrgRole == "" {
			return orgOutsiderExpectation(row)
		}
	default:
		return Allow
	}
	if row.MinRole != "" && !auth.OrgRoleAtLeast(p.OrgRole, row.MinRole) {
		return Deny403
	}
	return Allow
}

// orgOutsiderExpectation is what a non-member sees on an org route. Reads hide
// the org behind a 404; writes check the org permission first and answer 403,
// which discloses nothing a member could not already see.
func orgOutsiderExpectation(row Row) Expectation {
	if row.MinRole == "" {
		return Deny404
	}
	return Deny403
}

// isWrite reports whether the method mutates, which is what a read-only
// token's scopes refuse.
func isWrite(method string) bool {
	return method != http.MethodGet && method != http.MethodHead
}
