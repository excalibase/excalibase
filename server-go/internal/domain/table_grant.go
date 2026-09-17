package domain

import "time"

// TableGrant is one entry of a project's exposure list (EXC-370): permission
// for Role to run Operations against Resource through the generated
// GraphQL / REST surface. Absence of a matching enabled grant means the
// resource is not reachable while the project has exposure enforced.
type TableGrant struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Resource   string      `json:"resource"` // schema-qualified table or function
	Operations []Operation `json:"operations"`
	Role       string      `json:"role"` // "*" = every role
	Enabled    bool        `json:"enabled"`
	CreatedAt  *time.Time  `json:"createdAt,omitempty"`
	UpdatedAt  *time.Time  `json:"updatedAt,omitempty"`
}

// TableGrantSet is the wire shape of GET /api/provision/{projectId}/table-grants/.
//
// Enforced is carried explicitly and is never inferred from len(Grants): a
// client must be able to tell "exposure is not configured for this project,
// do not enforce anything" (enforced=false) from "exposure is enforced and
// nothing is granted, deny everything" (enforced=true, grants=[]). Grants is
// always a JSON array, never null.
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
