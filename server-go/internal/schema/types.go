package schema

type TableInfo struct {
	Name    string  `json:"name"`
	Schema  string  `json:"schema"`
	Type    string  `json:"type"`
	Comment *string `json:"comment"`
}

type ColumnInfo struct {
	Name                   string  `json:"name"`
	DataType               string  `json:"dataType"`
	Nullable               bool    `json:"nullable"`
	PrimaryKey             bool    `json:"primaryKey"`
	Unique                 bool    `json:"unique"`
	DefaultValue           *string `json:"defaultValue"`
	OrdinalPosition        int     `json:"ordinalPosition"`
	Comment                *string `json:"comment"`
	CharacterMaximumLength *int    `json:"characterMaximumLength"`
	NumericPrecision       *int    `json:"numericPrecision"`
	NumericScale           *int    `json:"numericScale"`
}

type RelationshipInfo struct {
	ConstraintName string `json:"constraintName"`
	SourceTable    string `json:"sourceTable"`
	SourceColumn   string `json:"sourceColumn"`
	TargetTable    string `json:"targetTable"`
	TargetColumn   string `json:"targetColumn"`
	OnDelete       string `json:"onDelete"`
	OnUpdate       string `json:"onUpdate"`
}

type IndexInfo struct {
	Name      string   `json:"name"`
	TableName string   `json:"tableName"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Type      string   `json:"type"`
}

type DDLResult struct {
	Success     bool   `json:"success"`
	UpdateCount int    `json:"updateCount,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ColumnMeta struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
}

type QueryResult struct {
	Columns      []ColumnMeta    `json:"columns,omitempty"`
	Rows         [][]interface{} `json:"rows,omitempty"`
	Command      string          `json:"command,omitempty"`
	AffectedRows int64           `json:"affectedRows,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// --- Table CRUD types ---

type CreateTableRequest struct {
	Name    string            `json:"name"`
	Schema  string            `json:"schema"`
	Comment string            `json:"comment,omitempty"`
	Columns []CreateColumnDef `json:"columns,omitempty"`
}

type CreateColumnDef struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default,omitempty"`
	PrimaryKey bool    `json:"primaryKey"`
	Unique     bool    `json:"unique"`
}

type UpdateTableRequest struct {
	NewName    *string `json:"newName,omitempty"`
	NewSchema  *string `json:"newSchema,omitempty"`
	RlsEnabled *bool   `json:"rlsEnabled,omitempty"`
	Comment    *string `json:"comment,omitempty"`
}

// --- Column CRUD types ---

type AddColumnRequest struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
	Unique   bool    `json:"unique"`
}

type AlterColumnRequest struct {
	NewName     *string `json:"newName,omitempty"`
	Type        *string `json:"type,omitempty"`
	Nullable    *bool   `json:"nullable,omitempty"`
	Default     *string `json:"default,omitempty"`
	DropDefault bool    `json:"dropDefault,omitempty"`
}

// --- Role types ---

type RoleInfo struct {
	Name       string `json:"name"`
	Login      bool   `json:"login"`
	Superuser  bool   `json:"superuser"`
	CreateDB   bool   `json:"createDb"`
	CreateRole bool   `json:"createRole"`
	ConnLimit  int    `json:"connLimit"`
}

type CreateRoleRequest struct {
	Name     string  `json:"name"`
	Password *string `json:"password,omitempty"`
	Login    bool    `json:"login"`
}

// --- Extension types ---

type ExtensionInfo struct {
	Name             string  `json:"name"`
	InstalledVersion *string `json:"installedVersion"`
	DefaultVersion   string  `json:"defaultVersion"`
	Schema           *string `json:"schema"`
	Comment          *string `json:"comment"`
}

// --- Policy types ---

type PolicyInfo struct {
	Name       string  `json:"name"`
	Table      string  `json:"table"`
	Command    string  `json:"command"`
	Roles      string  `json:"roles"`
	Using      *string `json:"using"`
	WithCheck  *string `json:"withCheck"`
	Permissive bool    `json:"permissive"`
}

type CreatePolicyRequest struct {
	Name       string  `json:"name"`
	Table      string  `json:"table"`
	Schema     string  `json:"schema"`
	Command    string  `json:"command"`
	Roles      string  `json:"roles"`
	Using      *string `json:"using,omitempty"`
	WithCheck  *string `json:"withCheck,omitempty"`
	Permissive bool    `json:"permissive"`
}

// --- Function types ---

type FunctionInfo struct {
	Name       string `json:"name"`
	Schema     string `json:"schema"`
	Language   string `json:"language"`
	ReturnType string `json:"returnType"`
	ArgTypes   string `json:"argTypes"`
	Volatility string `json:"volatility"`
	Definition string `json:"definition"`
}

type CreateFunctionRequest struct {
	Name       string `json:"name"`
	Schema     string `json:"schema"`
	Language   string `json:"language"`
	ReturnType string `json:"returnType"`
	Args       string `json:"args"`
	Body       string `json:"body"`
	Volatility string `json:"volatility"`
}

// --- Trigger types ---

type TriggerInfo struct {
	Name       string `json:"name"`
	Table      string `json:"table"`
	Schema     string `json:"schema"`
	Event      string `json:"event"`
	Timing     string `json:"timing"`
	ForEachRow bool   `json:"forEachRow"`
	Function   string `json:"function"`
	Enabled    bool   `json:"enabled"`
}

type CreateTriggerRequest struct {
	Name       string `json:"name"`
	Table      string `json:"table"`
	Schema     string `json:"schema"`
	Event      string `json:"event"`
	Timing     string `json:"timing"`
	ForEachRow bool   `json:"forEachRow"`
	Function   string `json:"function"`
}

// --- Index create/drop types ---

type CreateIndexRequest struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Schema  string   `json:"schema"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Type    string   `json:"type"`
}

// --- PostgreSQL type browser types ---

type PgTypeInfo struct {
	Name   string   `json:"name"`
	Schema string   `json:"schema"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// --- Advisor types ---

// AdvisorFinding represents a single lint finding from performance or security advisors.
type AdvisorFinding struct {
	RuleID      string `json:"ruleId"`
	Severity    string `json:"severity"`    // critical, high, medium, low
	Category    string `json:"category"`    // performance, security
	Title       string `json:"title"`
	Description string `json:"description"`
	Table       string `json:"table,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Fix         string `json:"fix,omitempty"`
}

// --- Row data types ---

// RowQueryOpts controls pagination, sorting, and filtering for GetRows.
type RowQueryOpts struct {
	Limit   int         // default 50, max 1000
	Offset  int         // default 0
	Sort    string      // column name to sort by
	Order   string      // "asc" or "desc"
	Filters []RowFilter // WHERE conditions
}

// RowFilter represents a single WHERE condition.
type RowFilter struct {
	Column   string // column name
	Operator string // =, !=, >, <, >=, <=, like, ilike, is_null, is_not_null
	Value    string // value to compare (ignored for is_null/is_not_null)
}

// RowsResult is the response from GetRows containing columns, rows, and total count.
type RowsResult struct {
	Columns    []ColumnMeta    `json:"columns"`
	Rows       [][]interface{} `json:"rows"`
	TotalCount int64           `json:"totalCount"`
}
