package domain

import "time"

// PolicyChangeEvent is the NATS payload published on
// "policies.{projectId}.changed" after every successful policy write.
// Lives in domain so both the handler (producer) and the publisher
// (consumer of the type) can reference it without an import cycle.
type PolicyChangeEvent struct {
	ProjectID string `json:"projectId"`
	Kind      string `json:"kind"`     // "rls" | "column" | "grant" | "exposure" | "project" | "credential"
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

// Operation mirrors io.github.excalibase.rls.Operation in the engine.
type Operation string

const (
	OpSelect Operation = "SELECT"
	OpInsert Operation = "INSERT"
	OpUpdate Operation = "UPDATE"
	OpDelete Operation = "DELETE"
)

// PolicyEffect mirrors io.github.excalibase.rls.PolicyEffect.
type PolicyEffect string

const (
	EffectAllow PolicyEffect = "ALLOW"
	EffectDeny  PolicyEffect = "DENY"
)

// LogicOperator mirrors io.github.excalibase.rls.LogicOperator.
type LogicOperator string

const (
	LogicAnd LogicOperator = "AND"
	LogicOr  LogicOperator = "OR"
)

// FieldType mirrors io.github.excalibase.rls.FieldType.
type FieldType string

const (
	FieldString   FieldType = "STRING"
	FieldUUID     FieldType = "UUID"
	FieldInteger  FieldType = "INTEGER"
	FieldLong     FieldType = "LONG"
	FieldBoolean  FieldType = "BOOLEAN"
	FieldDouble   FieldType = "DOUBLE"
	FieldDate     FieldType = "DATE"
	FieldDatetime FieldType = "DATETIME"
)

// RuleOperator mirrors io.github.excalibase.rls.RuleOperator.
type RuleOperator string

const (
	OpEQ        RuleOperator = "EQ"
	OpNEQ       RuleOperator = "NEQ"
	OpGT        RuleOperator = "GT"
	OpGTE       RuleOperator = "GTE"
	OpLT        RuleOperator = "LT"
	OpLTE       RuleOperator = "LTE"
	OpIN        RuleOperator = "IN"
	OpNOTIN     RuleOperator = "NOT_IN"
	OpLIKE      RuleOperator = "LIKE"
	OpNOTLIKE   RuleOperator = "NOT_LIKE"
	OpISNULL    RuleOperator = "IS_NULL"
	OpISNOTNULL RuleOperator = "IS_NOT_NULL"
)

// TargetType mirrors io.github.excalibase.rls.TargetType.
type TargetType string

const (
	TargetAll   TargetType = "ALL"
	TargetRole  TargetType = "ROLE"
	TargetUser  TargetType = "USER"
	TargetGroup TargetType = "GROUP"
)

// MaskMode mirrors io.github.excalibase.rls.MaskMode.
type MaskMode string

const (
	MaskHide    MaskMode = "HIDE"
	MaskNull    MaskMode = "NULL"
	MaskPartial MaskMode = "PARTIAL"
	MaskHash    MaskMode = "HASH"
	MaskCustom  MaskMode = "CUSTOM"
)

// Rule mirrors io.github.excalibase.rls.Rule. JSON tags match the Java
// record's property names so the engine can deserialize directly.
type Rule struct {
	Field     string       `json:"field"`
	FieldType FieldType    `json:"fieldType"`
	Operator  RuleOperator `json:"operator"`
	Value     string       `json:"value,omitempty"`
}

// Assignment mirrors io.github.excalibase.rls.Assignment.
type Assignment struct {
	TargetType TargetType `json:"targetType"`
	TargetID   string     `json:"targetId,omitempty"`
}

// PartialMaskSpec is a tagged union mirroring the Java sealed interface.
// Only one of the variant fields is populated; the discriminator is "kind".
// The Java side will deserialize each variant by inspecting kind.
type PartialMaskSpec struct {
	Kind        string `json:"kind"` // KEEP_FIRST | KEEP_LAST | KEEP_BOTH | MASK_RANGE | SUBSTRING | REGEX
	N           int    `json:"n,omitempty"`
	First       int    `json:"first,omitempty"`
	Last        int    `json:"last,omitempty"`
	Start       int    `json:"start,omitempty"`
	End         int    `json:"end,omitempty"`
	Length      int    `json:"length,omitempty"`
	MaskChar    string `json:"maskChar,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	Replacement string `json:"replacement,omitempty"`
}

// Policy mirrors io.github.excalibase.rls.Policy.
type Policy struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"projectId"`
	Name        string        `json:"name"`
	Resource    string        `json:"resource"`
	Effect      PolicyEffect  `json:"effect"`
	Operations  []Operation   `json:"operations"`
	RuleLogic   LogicOperator `json:"ruleLogic"`
	Priority    int           `json:"priority"`
	Enabled     bool          `json:"enabled"`
	Rules       []Rule        `json:"rules"`
	Assignments []Assignment  `json:"assignments"`
	CreatedAt   *time.Time    `json:"createdAt,omitempty"`
	UpdatedAt   *time.Time    `json:"updatedAt,omitempty"`
}

// ColumnPolicy mirrors io.github.excalibase.rls.ColumnPolicy.
type ColumnPolicy struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"projectId"`
	Name            string           `json:"name"`
	Resource        string           `json:"resource"`
	Columns         []string         `json:"columns"`
	Operations      []Operation      `json:"operations"`
	Mode            MaskMode         `json:"mode"`
	PartialSpec     *PartialMaskSpec `json:"partialSpec,omitempty"`
	CustomMaskerKey string           `json:"customMaskerKey,omitempty"`
	Priority        int              `json:"priority"`
	Enabled         bool             `json:"enabled"`
	Assignments     []Assignment     `json:"assignments"`
	CreatedAt       *time.Time       `json:"createdAt,omitempty"`
	UpdatedAt       *time.Time       `json:"updatedAt,omitempty"`
}
