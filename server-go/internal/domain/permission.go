package domain

import "encoding/json"

// API permissions (EXC-370 step C): one object per (table, role, operation),
// Hasura-style. The engine reads a project's whole set as one PermissionDocument
// from GET /api/provision/{projectId}/permissions/.

// A permission names the role the engine runs the caller as: anon with no
// session, user for a signed-in end user, or a custom role from the token.
const (
	// PermissionRolePattern is the shape of a role name; the migrations'
	// CHECK constraints enforce the same pattern.
	PermissionRolePattern = `^[a-z][a-z0-9_]{0,62}$`
	// PermissionRoleService bypasses permissions, so none may name it.
	PermissionRoleService = "service"
)

// The four operations a table permission is written for.
const (
	PermissionSelect = "select"
	PermissionInsert = "insert"
	PermissionUpdate = "update"
	PermissionDelete = "delete"
)

// How a tracked function is exposed; decided by its volatility, never chosen.
const (
	ExposeAsQuery    = "QUERY"
	ExposeAsMutation = "MUTATION"
)

// The Kind carried on PolicyChangeEvent for writes to this store.
const (
	PermissionChangeKind = "permission"
	FunctionChangeKind   = "function"
)

// TablePermission is one stored permission object. Definition is the
// validated operation object, e.g. {"filter":{},"columns":"*"}.
type TablePermission struct {
	ProjectID  string
	Table      string
	Role       string
	Operation  string
	Definition json.RawMessage
}

// TablePermissions is one (table, role) entry of the document; an operation
// key is present only when that permission exists.
type TablePermissions struct {
	Table  string          `json:"table"`
	Role   string          `json:"role"`
	Select json.RawMessage `json:"select,omitempty"`
	Insert json.RawMessage `json:"insert,omitempty"`
	Update json.RawMessage `json:"update,omitempty"`
	Delete json.RawMessage `json:"delete,omitempty"`
}

// TrackedFunction is a database function the project exposes.
type TrackedFunction struct {
	ProjectID        string  `json:"-"`
	Function         string  `json:"function"`
	ExposedAs        string  `json:"exposedAs"`
	InferPermissions bool    `json:"inferPermissions"`
	SessionArgument  *string `json:"sessionArgument"`
}

// FunctionPermission lets a role call a tracked function.
type FunctionPermission struct {
	Function string `json:"function"`
	Role     string `json:"role"`
}

// PermissionDocument is the wire shape of GET permissions/. Every slice is
// non-nil so it encodes as [] and never as null.
type PermissionDocument struct {
	ProjectID           string               `json:"projectId"`
	Version             int64                `json:"version"`
	Tables              []TablePermissions   `json:"tables"`
	Functions           []TrackedFunction    `json:"functions"`
	FunctionPermissions []FunctionPermission `json:"functionPermissions"`
}
