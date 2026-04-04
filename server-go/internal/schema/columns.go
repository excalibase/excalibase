package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AddColumn adds a new column to an existing table.
func (i *Introspector) AddColumn(ctx context.Context, db *sql.DB, schema, table string, req AddColumnRequest) error {
	var b strings.Builder
	b.WriteString("ALTER TABLE ")
	b.WriteString(QuoteIdent(schema))
	b.WriteString(".")
	b.WriteString(QuoteIdent(table))
	b.WriteString(" ADD COLUMN ")
	if err := ValidateTypeName(req.Type); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	b.WriteString(QuoteIdent(req.Name))
	b.WriteString(" ")
	b.WriteString(req.Type)

	if !req.Nullable {
		b.WriteString(" NOT NULL")
	}
	if req.Default != nil {
		safeDefault, err := ValidateDefaultExpression(*req.Default)
		if err != nil {
			return fmt.Errorf("add column default: %w", err)
		}
		b.WriteString(" DEFAULT ")
		b.WriteString(safeDefault)
	}
	if req.Unique {
		b.WriteString(" UNIQUE")
	}

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	return nil
}

// AlterColumn modifies an existing column's properties.
func (i *Introspector) AlterColumn(ctx context.Context, db *sql.DB, schema, table, col string, req AlterColumnRequest) error {
	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)
	quotedCol := QuoteIdent(col)

	if req.Type != nil {
		if err := ValidateTypeName(*req.Type); err != nil {
			return fmt.Errorf("alter column: %w", err)
		}
		stmt := "ALTER TABLE " + fqn + " ALTER COLUMN " + quotedCol +
			" TYPE " + *req.Type
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("alter column type: %w", err)
		}
	}

	if req.Nullable != nil {
		var action string
		if *req.Nullable {
			action = "DROP NOT NULL"
		} else {
			action = "SET NOT NULL"
		}
		stmt := "ALTER TABLE " + fqn + " ALTER COLUMN " + quotedCol + " " + action
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("alter column nullable: %w", err)
		}
	}

	if req.DropDefault {
		stmt := "ALTER TABLE " + fqn + " ALTER COLUMN " + quotedCol + " DROP DEFAULT"
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop column default: %w", err)
		}
	} else if req.Default != nil {
		safeDefault, err := ValidateDefaultExpression(*req.Default)
		if err != nil {
			return fmt.Errorf("alter column default: %w", err)
		}
		stmt := "ALTER TABLE " + fqn + " ALTER COLUMN " + quotedCol +
			" SET DEFAULT " + safeDefault
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("set column default: %w", err)
		}
	}

	if req.NewName != nil {
		stmt := "ALTER TABLE " + fqn + " RENAME COLUMN " + quotedCol +
			" TO " + QuoteIdent(*req.NewName)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rename column: %w", err)
		}
	}

	return nil
}

// DropColumn removes a column from a table.
func (i *Introspector) DropColumn(ctx context.Context, db *sql.DB, schema, table, col string) error {
	stmt := "ALTER TABLE " + QuoteIdent(schema) + "." + QuoteIdent(table) +
		" DROP COLUMN " + QuoteIdent(col)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop column: %w", err)
	}
	return nil
}
