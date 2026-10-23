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

// ddlStep is one statement of a multi-statement change and the label its
// failure is reported under.
type ddlStep struct {
	sql  string
	what string
}

// execSteps runs each step in order, stopping at the first failure.
func execSteps(ctx context.Context, db *sql.DB, steps []ddlStep) error {
	for _, step := range steps {
		if _, err := db.ExecContext(ctx, step.sql); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}
	return nil
}

// AddColumn adds a new column to an existing table.
func (i *Introspector) AddColumn(ctx context.Context, db *sql.DB, schema, table string, req AddColumnRequest) error {
	stmt, err := buildAddColumnSQL(schema, table, req)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	return nil
}

// buildAddColumnSQL constructs the ALTER TABLE ... ADD COLUMN DDL.
func buildAddColumnSQL(schema, table string, req AddColumnRequest) (string, error) {
	if err := CheckNewName("column", req.Name); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(sqlAlterTable)
	b.WriteString(QuoteIdent(schema))
	b.WriteString(".")
	b.WriteString(QuoteIdent(table))
	b.WriteString(" ADD COLUMN ")
	if err := ValidateTypeName(req.Type); err != nil {
		return "", fmt.Errorf("add column: %w", err)
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
			return "", fmt.Errorf("add column default: %w", err)
		}
		b.WriteString(" DEFAULT ")
		b.WriteString(safeDefault)
	}
	if req.Unique {
		b.WriteString(" UNIQUE")
	}
	return b.String(), nil
}

// AlterColumn modifies an existing column's properties.
func (i *Introspector) AlterColumn(ctx context.Context, db *sql.DB, schema, table, col string, req AlterColumnRequest) error {
	steps, err := buildAlterColumnSQL(schema, table, col, req)
	if err != nil {
		return err
	}
	return execSteps(ctx, db, steps)
}

// buildAlterColumnSQL constructs the statements AlterColumn runs, in order.
func buildAlterColumnSQL(schema, table, col string, req AlterColumnRequest) ([]ddlStep, error) {
	if err := checkOptionalNewName("column", req.NewName); err != nil {
		return nil, err
	}
	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)
	quotedCol := QuoteIdent(col)
	base := sqlAlterTable + fqn + sqlAlterColumn + quotedCol

	var steps []ddlStep
	if req.Type != nil {
		if err := ValidateTypeName(*req.Type); err != nil {
			return nil, fmt.Errorf("alter column: %w", err)
		}
		steps = append(steps, ddlStep{base + " TYPE " + *req.Type, "alter column type"})
	}
	if req.Nullable != nil {
		action := "SET NOT NULL"
		if *req.Nullable {
			action = "DROP NOT NULL"
		}
		steps = append(steps, ddlStep{base + " " + action, "alter column nullable"})
	}
	if req.DropDefault {
		steps = append(steps, ddlStep{base + " DROP DEFAULT", "drop column default"})
	} else if req.Default != nil {
		safeDefault, err := ValidateDefaultExpression(*req.Default)
		if err != nil {
			return nil, fmt.Errorf("alter column default: %w", err)
		}
		steps = append(steps, ddlStep{base + " SET DEFAULT " + safeDefault, "set column default"})
	}
	if req.NewName != nil {
		steps = append(steps, ddlStep{sqlAlterTable + fqn + " RENAME COLUMN " + quotedCol + " TO " + QuoteIdent(*req.NewName), "rename column"})
	}
	return steps, nil
}

// DropColumn removes a column from a table.
func (i *Introspector) DropColumn(ctx context.Context, db *sql.DB, schema, table, col string) error {
	if _, err := db.ExecContext(ctx, buildDropColumnSQL(schema, table, col)); err != nil {
		return fmt.Errorf("drop column: %w", err)
	}
	return nil
}

func buildDropColumnSQL(schema, table, col string) string {
	return sqlAlterTable + QuoteIdent(schema) + "." + QuoteIdent(table) + " DROP COLUMN " + QuoteIdent(col)
}
