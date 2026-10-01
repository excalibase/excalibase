package domain

// PolicyChangeEvent is the NATS payload published on
// "policies.{projectId}.changed" after every successful policy write.
// Lives in domain so both the handler (producer) and the publisher
// (consumer of the type) can reference it without an import cycle.
type PolicyChangeEvent struct {
	ProjectID string `json:"projectId"`
	Kind      string `json:"kind"`     // "permission" | "function" | "project" | "credential" | "schema"
	Resource  string `json:"resource"` // what the change targets: a table, or a role for a credential change
	PolicyID  string `json:"policyId"`
	Op        string `json:"op"` // OpChangeCreate | OpChangeUpdate | OpChangeDelete
}

// The operations a PolicyChangeEvent carries. Consumers switch on these, so
// the set is closed: a change that replaces something that already exists —
// a rotated credential included — is an update, not a verb of its own.
const (
	OpChangeCreate = "create"
	OpChangeUpdate = "update"
	OpChangeDelete = "delete"
)

// CredentialChangeKind is the Kind carried on PolicyChangeEvent when a
// project's role passwords are rotated. The engine and the auth service cache
// the credentials they read from vault with a TTL, so without this event they
// keep a password the database has already stopped accepting until that TTL
// runs out. It travels on the same "policies.{projectId}.changed" subject as
// every other cache-invalidating change.
const CredentialChangeKind = "credential"

// SchemaChangeKind is the Kind carried when DDL reshapes a project's tables.
// The engine caches a project's schema for thirty minutes and evicts it on
// this subject, so without the event a new table stays invisible until the
// TTL runs out (EXC-437).
const SchemaChangeKind = "schema"
