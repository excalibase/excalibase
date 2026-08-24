package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetPolicies returns all RLS policies for tables in the given schema.
func (i *Introspector) GetPolicies(ctx context.Context, db *sql.DB, schema string) ([]PolicyInfo, error) {
	rows, err := db.QueryContext(ctx, policiesQuery, schema)
	if err != nil {
		return nil, fmt.Errorf("query policies: %w", err)
	}
	defer rows.Close()

	policies := make([]PolicyInfo, 0)
	for rows.Next() {
		var p PolicyInfo
		var permissive string
		var roles string
		if err := rows.Scan(&p.Name, &p.Table, &p.Command, &roles, &p.Using, &p.WithCheck, &permissive); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		p.Permissive = permissive == "PERMISSIVE"
		// pg_policies returns roles as array literal like {public}, clean it
		p.Roles = strings.Trim(roles, "{}")
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

// CreatePolicy creates a new RLS policy on a table.
func (i *Introspector) CreatePolicy(ctx context.Context, db *sql.DB, req CreatePolicyRequest) error {
	schemaName := req.Schema
	if schemaName == "" {
		schemaName = "public"
	}

	var b strings.Builder
	b.WriteString("CREATE POLICY ")
	b.WriteString(QuoteIdent(req.Name))
	b.WriteString(" ON ")
	b.WriteString(QuoteIdent(schemaName))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Table))

	if req.Permissive {
		b.WriteString(" AS PERMISSIVE")
	} else {
		b.WriteString(" AS RESTRICTIVE")
	}

	if err := ValidatePolicyCommand(req.Command); err != nil {
		return err
	}
	b.WriteString(" FOR ")
	b.WriteString(strings.ToUpper(req.Command))

	b.WriteString(" TO ")
	b.WriteString(QuoteRoles(req.Roles))

	if req.Using != nil {
		safe, err := ValidatePolicyExpression(*req.Using)
		if err != nil {
			return fmt.Errorf("USING: %w", err)
		}
		b.WriteString(" USING (")
		b.WriteString(safe)
		b.WriteString(")")
	}
	if req.WithCheck != nil {
		safe, err := ValidatePolicyExpression(*req.WithCheck)
		if err != nil {
			return fmt.Errorf("WITH CHECK: %w", err)
		}
		b.WriteString(" WITH CHECK (")
		b.WriteString(safe)
		b.WriteString(")")
	}

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("create policy: %w", err)
	}
	return nil
}

// DropPolicy drops a policy from a table.
func (i *Introspector) DropPolicy(ctx context.Context, db *sql.DB, table, name string) error {
	stmt := "DROP POLICY " + QuoteIdent(name) + " ON " + QuoteIdent(table)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop policy: %w", err)
	}
	return nil
}

const policiesQuery = `
SELECT
    policyname,
    tablename,
    cmd,
    roles::text,
    qual,
    with_check,
    permissive
FROM pg_policies
WHERE schemaname = $1
ORDER BY tablename, policyname`
