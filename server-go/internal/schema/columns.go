package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	sqlAlterTable  = "ALTER TABLE "
	sqlAlterColumn = " ALTER COLUMN "
)

// AddColumn adds a new column to an existing table.
func (i *Introspector) AddColumn(ctx context.Context, db *sql.DB, schema, table string, req AddColumnRequest) error {
	var b strings.Builder
	b.WriteString(sqlAlterTable)
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
	base := sqlAlterTable + fqn + sqlAlterColumn + quotedCol

	if err := alterColumnType(ctx, db, base, req); err != nil {
		return err
	}
	if err := alterColumnNullable(ctx, db, base, req); err != nil {
		return err
	}
	if err := alterColumnDefault(ctx, db, base, req); err != nil {
		return err
	}
	if req.NewName != nil {
		stmt := sqlAlterTable + fqn + " RENAME COLUMN " + quotedCol + " TO " + QuoteIdent(*req.NewName)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rename column: %w", err)
		}
	}
	return nil
}

func alterColumnType(ctx context.Context, db *sql.DB, base string, req AlterColumnRequest) error {
	if req.Type == nil {
		return nil
	}
	if err := ValidateTypeName(*req.Type); err != nil {
		return fmt.Errorf("alter column: %w", err)
	}
	_, err := db.ExecContext(ctx, base+" TYPE "+*req.Type)
	if err != nil {
		return fmt.Errorf("alter column type: %w", err)
	}
	return nil
}

func alterColumnNullable(ctx context.Context, db *sql.DB, base string, req AlterColumnRequest) error {
	if req.Nullable == nil {
		return nil
	}
	action := "SET NOT NULL"
	if *req.Nullable {
		action = "DROP NOT NULL"
	}
	if _, err := db.ExecContext(ctx, base+" "+action); err != nil {
		return fmt.Errorf("alter column nullable: %w", err)
	}
	return nil
}

func alterColumnDefault(ctx context.Context, db *sql.DB, base string, req AlterColumnRequest) error {
	if req.DropDefault {
		if _, err := db.ExecContext(ctx, base+" DROP DEFAULT"); err != nil {
			return fmt.Errorf("drop column default: %w", err)
		}
		return nil
	}
	if req.Default == nil {
		return nil
	}
	safeDefault, err := ValidateDefaultExpression(*req.Default)
	if err != nil {
		return fmt.Errorf("alter column default: %w", err)
	}
	if _, err := db.ExecContext(ctx, base+" SET DEFAULT "+safeDefault); err != nil {
		return fmt.Errorf("set column default: %w", err)
	}
	return nil
}

// DropColumn removes a column from a table.
func (i *Introspector) DropColumn(ctx context.Context, db *sql.DB, schema, table, col string) error {
	stmt := sqlAlterTable + QuoteIdent(schema) + "." + QuoteIdent(table) +
		" DROP COLUMN " + QuoteIdent(col)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop column: %w", err)
	}
	return nil
}
