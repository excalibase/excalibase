package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/graphqlnames"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// InvalidPermissionError is a permission the engine would drop and log as
// permission_invalid (excalibase-graphql RulesResolver), with its reason.
type InvalidPermissionError struct{ Reason string }

func (e *InvalidPermissionError) Error() string { return "permission_invalid: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidPermissionError{Reason: fmt.Sprintf(format, args...)}
}

// TableShape is one table or view of a schema with its column names.
type TableShape struct {
	Name    string
	View    bool
	Columns []string
}

// KeyColumn is one column of a foreign key touching a schema.
type KeyColumn struct {
	Constraint, SourceSchema, SourceTable, SourceColumn, TargetSchema, TargetTable string
}

// LiveSchema is one schema of a project's database (on MySQL, its one
// database) as the engine reflects it.
type LiveSchema struct {
	Name string
	// Database is a MySQL project's database, the only schema it has.
	Database string
	tables   map[string]liveTable
}

type liveTable struct {
	view    bool
	columns map[string]bool
	// relationships maps each field name, canonical or alias, to its target.
	relationships map[string]string
	// crossSchema marks a table with a key to or from another schema, whose
	// field names are left to the engine.
	crossSchema bool
}

// NewLiveSchema names each table's relationships as the engine does.
func NewLiveSchema(name, database string, tables []TableShape, keys []KeyColumn) *LiveSchema {
	live := &LiveSchema{Name: name, Database: database, tables: make(map[string]liveTable, len(tables))}
	columns := make(map[string][]string, len(tables))
	for _, table := range tables {
		set := make(map[string]bool, len(table.Columns))
		for _, column := range table.Columns {
			set[column] = true
		}
		live.tables[table.Name] = liveTable{view: table.View, columns: set, relationships: map[string]string{}}
		columns[table.Name] = table.Columns
	}
	var rows []schema.RelationshipInfo
	for _, key := range keys {
		if key.SourceSchema != name || key.TargetSchema != name {
			live.markCrossSchema(key)
			continue
		}
		rows = append(rows, schema.RelationshipInfo{ConstraintName: key.Constraint, SourceTable: key.SourceTable,
			SourceColumn: key.SourceColumn, TargetTable: key.TargetTable})
	}
	for table, fields := range graphqlnames.Relationships(name, columns, rows) {
		for _, field := range fields {
			live.tables[table].relationships[field.Field] = field.Table
			if field.DeprecatedName != "" {
				live.tables[table].relationships[field.DeprecatedName] = field.Table
			}
		}
	}
	return live
}

func (s *LiveSchema) markCrossSchema(key KeyColumn) {
	for _, end := range [][2]string{{key.SourceSchema, key.SourceTable}, {key.TargetSchema, key.TargetTable}} {
		if table, ok := s.tables[end[1]]; ok && end[0] == s.Name {
			table.crossSchema = true
			s.tables[end[1]] = table
		}
	}
}

// SchemaSource reads one schema of the project's database.
type SchemaSource func(ctx context.Context, schemaName string) (*LiveSchema, error)

// CheckLive refuses a normalized permission that names a table, column or
// relationship the database does not have, or writes to a view: the engine
// would drop it, hiding the table from the role. A failed read is returned
// as it is, never taken as a pass.
func CheckLive(ctx context.Context, source SchemaSource, table, operation string, definition json.RawMessage) error {
	checker := &liveChecker{ctx: ctx, source: source, schemas: map[string]*LiveSchema{}}
	shape, err := checker.table(table)
	if err != nil {
		return err
	}
	if shape.view && operation != "select" {
		return invalid("%s is a view; only a select permission applies to it", table)
	}
	fields, err := decodeObject(definition)
	if err != nil {
		return err
	}
	for key, value := range fields {
		if err := checker.field(table, shape, key, value); err != nil {
			return err
		}
	}
	return nil
}

type liveChecker struct {
	ctx     context.Context
	source  SchemaSource
	schemas map[string]*LiveSchema
}

func (c *liveChecker) table(key string) (liveTable, error) {
	schemaName, name, _ := strings.Cut(key, ".")
	live, ok := c.schemas[schemaName]
	if !ok {
		read, err := c.source(c.ctx, schemaName)
		if err != nil {
			return liveTable{}, err
		}
		live, c.schemas[schemaName] = read, read
	}
	shape, ok := live.tables[name]
	if ok {
		return shape, nil
	}
	if live.Database != "" && live.Database != schemaName {
		return liveTable{}, invalid("Unknown table %s: a MySQL project's tables are in its database %s, e.g. %s.%s",
			key, live.Database, live.Database, name)
	}
	return liveTable{}, invalid("Unknown table %s", key)
}

func (c *liveChecker) field(table string, shape liveTable, key string, value json.RawMessage) error {
	switch key {
	case keyFilter, keyCheck:
		return c.expression(table, shape, value)
	case keyColumns:
		var names []string
		if json.Unmarshal(value, &names) != nil {
			return nil // "*"
		}
		return knownColumns(table, shape, names)
	case keySet:
		presets, err := decodeObject(value)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(presets))
		for column := range presets {
			names = append(names, column)
		}
		return knownColumns(table, shape, names)
	}
	return nil
}

func knownColumns(table string, shape liveTable, names []string) error {
	for _, name := range names {
		if !shape.columns[name] {
			return invalid("Unknown column %s on %s", name, table)
		}
	}
	return nil
}

// expression walks a filter or check the way ValidateBoolExp reads it.
func (c *liveChecker) expression(table string, shape liveTable, raw json.RawMessage) error {
	fields, err := decodeObject(raw)
	if err != nil {
		return err
	}
	for key, value := range fields {
		if err := c.term(table, shape, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (c *liveChecker) term(table string, shape liveTable, key string, value json.RawMessage) error {
	switch key {
	case opAnd, opOr:
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := c.expression(table, shape, item); err != nil {
				return err
			}
		}
		return nil
	case opNot:
		return c.expression(table, shape, value)
	case opExists:
		return c.exists(value)
	}
	ops, err := decodeObject(value)
	if err != nil {
		return err
	}
	for op := range ops {
		if isComparisonOperator(op) {
			return knownColumns(table, shape, []string{key})
		}
	}
	return c.relationship(table, shape, key, value)
}

func (c *liveChecker) relationship(table string, shape liveTable, name string, where json.RawMessage) error {
	target, ok := shape.relationships[name]
	if !ok {
		if shape.crossSchema {
			return nil
		}
		return invalid("Unknown relationship %s on %s", name, table)
	}
	schemaName, _, _ := strings.Cut(table, ".")
	targetKey := schemaName + "." + target
	targetShape, err := c.table(targetKey)
	if err != nil {
		return err
	}
	return c.expression(targetKey, targetShape, where)
}

func (c *liveChecker) exists(value json.RawMessage) error {
	var exists struct {
		Table string          `json:"_table"`
		Where json.RawMessage `json:"_where"`
	}
	if err := json.Unmarshal(value, &exists); err != nil {
		return err
	}
	shape, err := c.table(exists.Table)
	if err != nil {
		return err
	}
	return c.expression(exists.Table, shape, exists.Where)
}
