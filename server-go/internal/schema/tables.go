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

	sql, err := buildCreateTableSQL(schema, req)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, sql); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	if req.Comment != "" {
		commentSQL := "COMMENT ON TABLE " + QuoteIdent(schema) + "." + QuoteIdent(req.Name) +
			" IS " + QuoteLiteral(req.Comment)
		if _, err := db.ExecContext(ctx, commentSQL); err != nil {
			return fmt.Errorf("set table comment: %w", err)
		}
	}
	return nil
}

// buildCreateTableSQL constructs the CREATE TABLE DDL without executing it.
func buildCreateTableSQL(schema string, req CreateTableRequest) (string, error) {
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
	fqn := QuoteIdent(schema) + "." + QuoteIdent(name)

	if req.RlsEnabled != nil {
		var action string
		if *req.RlsEnabled {
			action = "ENABLE"
		} else {
			action = "DISABLE"
		}
		sql := sqlAlterTable + fqn + " " + action + " ROW LEVEL SECURITY"
		if _, err := db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("toggle RLS: %w", err)
		}
	}

	if req.Comment != nil {
		commentSQL := "COMMENT ON TABLE " + fqn + " IS " + QuoteLiteral(*req.Comment)
		if _, err := db.ExecContext(ctx, commentSQL); err != nil {
			return fmt.Errorf("set comment: %w", err)
		}
	}

	if req.NewSchema != nil {
		sql := sqlAlterTable + fqn + " SET SCHEMA " + QuoteIdent(*req.NewSchema)
		if _, err := db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("move schema: %w", err)
		}
		// Update schema for subsequent operations
		schema = *req.NewSchema
	}

	if req.NewName != nil {
		currentFQN := QuoteIdent(schema) + "." + QuoteIdent(name)
		sql := sqlAlterTable + currentFQN + " RENAME TO " + QuoteIdent(*req.NewName)
		if _, err := db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("rename table: %w", err)
		}
	}

	return nil
}

// DropTable drops a table, optionally with CASCADE.
func (i *Introspector) DropTable(ctx context.Context, db *sql.DB, schema, name string, cascade bool) error {
	sql := "DROP TABLE " + QuoteIdent(schema) + "." + QuoteIdent(name)
	if cascade {
		sql += " CASCADE"
	}
	if _, err := db.ExecContext(ctx, sql); err != nil {
		return fmt.Errorf("drop table: %w", err)
	}
	return nil
}
