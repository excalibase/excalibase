package domain

import "time"

// A grant names the role the engine runs the caller as (EXC-370): anon with
// no session, user for a signed-in end user, or a custom role carried in the
// token. Which rows that role sees stays with RLS.
const (
	// GrantRoleAnon is a caller with no token or a publishable-key token.
	GrantRoleAnon = "anon"
	// GrantRoleUser is a signed-in end user running as the account's role.
	GrantRoleUser = "user"
	// GrantRoleService bypasses exposure, so no grant may name it.
	GrantRoleService = "service"
)

// GrantRolePattern is the shape of a grantable role name; the migration's
// CHECK constraint enforces the same pattern.
const GrantRolePattern = `^[a-z][a-z0-9_]{0,62}$`

// TableGrant is one entry of a project's exposure list (EXC-370): permission
// for Role to run Operations against Resource through the generated
// GraphQL / REST surface. Absence of a matching enabled grant means the
// resource is not reachable — enforcement is on for every project (EXC-400).
type TableGrant struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Resource   string      `json:"resource"` // schema-qualified table or function
	Operations []Operation `json:"operations"`
	Role       string      `json:"role"` // anon, user or a custom role; never service
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
