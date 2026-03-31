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
