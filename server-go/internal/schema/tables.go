package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CreateTable creates a new table with optional columns and comment.
func (i *Introspector) CreateTable(ctx context.Context, db *sql.DB, req CreateTableRequest) error {
	schema := req.Schema
	if schema == "" {
		schema = "public"
	}
	if err := CheckNewName("schema", schema); err != nil {
		return err
	}

	sql, err := buildCreateTableSQL(schema, req)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, sql); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	if req.Comment != "" {
		if _, err := db.ExecContext(ctx, buildTableCommentSQL(schema, req.Name, req.Comment)); err != nil { // NOSONAR: identifiers quoted by QuoteIdent, the comment by QuoteLiteral
			return fmt.Errorf("set table comment: %w", err)
		}
	}
	return nil
}

// buildCreateTableSQL constructs the CREATE TABLE DDL without executing it.
func buildCreateTableSQL(schema string, req CreateTableRequest) (string, error) {
	if err := CheckNewName("table", req.Name); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(QuoteIdent(schema))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Name))

	if len(req.Columns) == 0 {
		b.WriteString(" ()")
		return b.String(), nil
	}

	b.WriteString(" (")
	pkCols, err := appendColumnDefs(&b, req.Columns)
	if err != nil {
		return "", err
	}
	if len(pkCols) > 0 {
		b.WriteString(", PRIMARY KEY (")
		b.WriteString(strings.Join(pkCols, ", "))
		b.WriteString(")")
	}
	b.WriteString(")")
	return b.String(), nil
}

// appendColumnDefs writes column definitions to b and returns any primary key column names.
func appendColumnDefs(b *strings.Builder, cols []CreateColumnDef) ([]string, error) {
	var pkCols []string
	for idx, col := range cols {
		isPK, err := writeColumnDef(b, col, idx)
		if err != nil {
			return nil, err
		}
		if isPK {
			pkCols = append(pkCols, QuoteIdent(col.Name))
		}
	}
	return pkCols, nil
}

// writeColumnDef writes a single column definition fragment to b.
// Returns true if the column is a primary key.
func writeColumnDef(b *strings.Builder, col CreateColumnDef, idx int) (isPK bool, err error) {
	if err := CheckNewName("column", col.Name); err != nil {
		return false, err
	}
	if err := ValidateTypeName(col.Type); err != nil {
		return false, fmt.Errorf("column %q: %w", col.Name, err)
	}
	if idx > 0 {
		b.WriteString(", ")
	}
	b.WriteString(QuoteIdent(col.Name))
	b.WriteString(" ")
	b.WriteString(col.Type)
	if !col.Nullable {
		b.WriteString(" NOT NULL")
	}
	if col.Default != nil {
		safeDefault, valErr := ValidateDefaultExpression(*col.Default)
		if valErr != nil {
			return false, fmt.Errorf("column %q default: %w", col.Name, valErr)
		}
		b.WriteString(" DEFAULT ")
		b.WriteString(safeDefault)
	}
	if col.Unique {
		b.WriteString(" UNIQUE")
	}
	return col.PrimaryKey, nil
}

// UpdateTable alters table properties: rename, move schema, toggle RLS, set comment.
func (i *Introspector) UpdateTable(ctx context.Context, db *sql.DB, schema, name string, req UpdateTableRequest) error {
	if err := checkOptionalNewName("table", req.NewName); err != nil {
		return err
	}
	if err := checkOptionalIdentifier("schema", req.NewSchema); err != nil {
		return err
	}
	return execSteps(ctx, db, buildUpdateTableSQL(schema, name, req))
}

// buildTableCommentSQL constructs COMMENT ON TABLE; the comment is a quoted literal.
func buildTableCommentSQL(schema, table, comment string) string {
	return "COMMENT ON TABLE " + QuoteIdent(schema) + "." + QuoteIdent(table) + " IS " + QuoteLiteral(comment)
}

// buildUpdateTableSQL constructs the statements UpdateTable runs, in order.
func buildUpdateTableSQL(schema, name string, req UpdateTableRequest) []ddlStep {
	fqn := QuoteIdent(schema) + "." + QuoteIdent(name)
	var steps []ddlStep
	if req.RlsEnabled != nil {
		action := "DISABLE"
		if *req.RlsEnabled {
			action = "ENABLE"
		}
		steps = append(steps, ddlStep{sqlAlterTable + fqn + " " + action + " ROW LEVEL SECURITY", "toggle RLS"})
	}
	if req.Comment != nil {
		steps = append(steps, ddlStep{buildTableCommentSQL(schema, name, *req.Comment), "set comment"})
	}
	if req.NewSchema != nil {
		steps = append(steps, ddlStep{sqlAlterTable + fqn + " SET SCHEMA " + QuoteIdent(*req.NewSchema), "move schema"})
		schema = *req.NewSchema
	}
	if req.NewName != nil {
		currentFQN := QuoteIdent(schema) + "." + QuoteIdent(name)
		steps = append(steps, ddlStep{sqlAlterTable + currentFQN + " RENAME TO " + QuoteIdent(*req.NewName), "rename table"})
	}
	return steps
}

// DropTable drops a table, optionally with CASCADE.
func (i *Introspector) DropTable(ctx context.Context, db *sql.DB, schema, name string, cascade bool) error {
	if _, err := db.ExecContext(ctx, buildDropTableSQL(schema, name, cascade)); err != nil { // NOSONAR: identifiers quoted by QuoteIdent; DDL cannot bind identifiers
		return fmt.Errorf("drop table: %w", err)
	}
	return nil
}

func buildDropTableSQL(schema, name string, cascade bool) string {
	stmt := "DROP TABLE " + QuoteIdent(schema) + "." + QuoteIdent(name)
	if cascade {
		stmt += " CASCADE"
	}
	return stmt
}
