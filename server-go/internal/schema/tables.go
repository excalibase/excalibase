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

	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(QuoteIdent(schema))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Name))

	if len(req.Columns) > 0 {
		b.WriteString(" (")
		var pkCols []string
		for idx, col := range req.Columns {
			if err := ValidateTypeName(col.Type); err != nil {
				return fmt.Errorf("column %q: %w", col.Name, err)
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
				safeDefault, err := ValidateDefaultExpression(*col.Default)
				if err != nil {
					return fmt.Errorf("column %q default: %w", col.Name, err)
				}
				b.WriteString(" DEFAULT ")
				b.WriteString(safeDefault)
			}
			if col.Unique {
				b.WriteString(" UNIQUE")
			}
			if col.PrimaryKey {
				pkCols = append(pkCols, QuoteIdent(col.Name))
			}
		}
		if len(pkCols) > 0 {
			b.WriteString(", PRIMARY KEY (")
			b.WriteString(strings.Join(pkCols, ", "))
			b.WriteString(")")
		}
		b.WriteString(")")
	} else {
		b.WriteString(" ()")
	}

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
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
		sql := "ALTER TABLE " + fqn + " " + action + " ROW LEVEL SECURITY"
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
		sql := "ALTER TABLE " + fqn + " SET SCHEMA " + QuoteIdent(*req.NewSchema)
		if _, err := db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("move schema: %w", err)
		}
		// Update schema for subsequent operations
		schema = *req.NewSchema
	}

	if req.NewName != nil {
		currentFQN := QuoteIdent(schema) + "." + QuoteIdent(name)
		sql := "ALTER TABLE " + currentFQN + " RENAME TO " + QuoteIdent(*req.NewName)
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
