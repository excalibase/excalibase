package domain

import "time"

// The only two roles an exposure grant may name (EXC-400). Exposure decides
// what an END USER of the tenant's API can reach, and an end user is either
// signed in or not. Anything finer — per-tenant, per-plan, per-department —
// is a row-level security policy, which is where a claim-driven rule belongs;
// putting it here would make the reachable schema itself depend on a free-form
// claim, and a schema that varies by claim is one an operator cannot read off
// the page in front of them.
const (
	// GrantRoleAnon is a caller with no session: no token, or a token the
	// engine could not verify.
	GrantRoleAnon = "anon"
	// GrantRoleAuthenticated is any caller holding a valid end-user token.
	GrantRoleAuthenticated = "authenticated"
)

// GrantRoles lists the accepted roles in the order they are offered.
var GrantRoles = []string{GrantRoleAnon, GrantRoleAuthenticated}

// TableGrant is one entry of a project's exposure list (EXC-370): permission
// for Role to run Operations against Resource through the generated
// GraphQL / REST surface. Absence of a matching enabled grant means the
// resource is not reachable — enforcement is on for every project (EXC-400).
type TableGrant struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Resource   string      `json:"resource"` // schema-qualified table or function
	Operations []Operation `json:"operations"`
	Role       string      `json:"role"` // GrantRoleAnon or GrantRoleAuthenticated
	Enabled    bool        `json:"enabled"`
	CreatedAt  *time.Time  `json:"createdAt,omitempty"`
	UpdatedAt  *time.Time  `json:"updatedAt,omitempty"`
}

// TableGrantSet is the wire shape of GET /api/provision/{projectId}/table-grants/.
//
// Enforced is carried explicitly and is never inferred from len(Grants): the
// engine must read a decision, not guess at one from an empty array.
// {"enforced":true,"grants":[]} means deny everything.
//
// Since EXC-400 it is true for every project. It is false only while the
// operator has switched exposure off for the whole installation with
// EXCALIBASE_EXPOSURE_ENFORCED=false; there is no per-project opt-in and no
// route that can write it. Grants is always a JSON array, never null.
type TableGrantSet struct {
	ProjectID string       `json:"projectId"`
	Enforced  bool         `json:"enforced"`
	Grants    []TableGrant `json:"grants"`
}

// GrantChangeKind is the Kind carried on PolicyChangeEvent for exposure
// writes, so the engine evicts cached policies and grants together off the
// single "policies.{projectId}.changed" subject.
const (
	GrantChangeKind    = "grant"
	ExposureChangeKind = "exposure"
)
