package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// RunPerformanceAdvisor runs all performance lint rules against pg_catalog.
func (i *Introspector) RunPerformanceAdvisor(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	findings := make([]AdvisorFinding, 0)

	fns := []func(context.Context, *sql.DB, string) ([]AdvisorFinding, error){
		i.checkUnindexedForeignKeys,
		i.checkNoPrimaryKey,
		i.checkUnusedIndexes,
		i.checkDuplicateIndexes,
	}

	for _, fn := range fns {
		results, err := fn(ctx, db, schema)
		if err != nil {
			return nil, err
		}
		findings = append(findings, results...)
	}

	return findings, nil
}

// RunSecurityAdvisor runs all security lint rules against pg_catalog.
func (i *Introspector) RunSecurityAdvisor(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	findings := make([]AdvisorFinding, 0)

	fns := []func(context.Context, *sql.DB, string) ([]AdvisorFinding, error){
		i.checkMultiplePermissivePolicies,
		i.checkPolicyExistsRLSDisabled,
		i.checkRLSEnabledNoPolicy,
		i.checkSecurityDefinerView,
		i.checkFunctionSearchPathMutable,
		i.checkRLSDisabledInPublic,
		i.checkExtensionInPublic,
	}

	for _, fn := range fns {
		results, err := fn(ctx, db, schema)
		if err != nil {
			return nil, err
		}
		findings = append(findings, results...)
	}

	return findings, nil
}

// --- Performance rules ---

// perf-0001: Unindexed Foreign Keys
func (i *Introspector) checkUnindexedForeignKeys(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT c.conname, t.relname AS table_name, a.attname AS column_name
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'f' AND n.nspname = $1
		AND NOT EXISTS (
			SELECT 1 FROM pg_index i
			WHERE i.indrelid = c.conrelid
			AND a.attnum = ANY(i.indkey)
		)`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check unindexed FKs: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var conname, table, column string
		if err := rows.Scan(&conname, &table, &column); err != nil {
			return nil, fmt.Errorf("scan unindexed FK: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "perf-0001",
			Severity:    "high",
			Category:    "performance",
			Title:       "Unindexed foreign key",
			Description: fmt.Sprintf("Foreign key %q on %s.%s has no index, causing slow joins and deletes on the referenced table.", conname, table, column),
			Table:       table,
			Detail:      fmt.Sprintf("constraint=%s column=%s", conname, column),
			Fix:         fmt.Sprintf(`CREATE INDEX ON "%s"("%s")`, table, column),
		})
	}
	return findings, rows.Err()
}

// perf-0004: No Primary Key
func (i *Introspector) checkNoPrimaryKey(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT c.relname AS table_name
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = $1
		AND NOT EXISTS (
			SELECT 1 FROM pg_constraint con
			WHERE con.conrelid = c.oid AND con.contype = 'p'
		)`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check no PK: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan no PK: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "perf-0004",
			Severity:    "high",
			Category:    "performance",
			Title:       "No primary key",
			Description: fmt.Sprintf("Table %q has no primary key. This prevents logical replication and makes row identification unreliable.", table),
			Table:       table,
			Fix:         fmt.Sprintf(`ALTER TABLE "%s" ADD PRIMARY KEY (id)`, table),
		})
	}
	return findings, rows.Err()
}

// perf-0005: Unused Index
func (i *Introspector) checkUnusedIndexes(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT s.indexrelname AS index_name, s.relname AS table_name, s.idx_scan
		FROM pg_stat_user_indexes s
		JOIN pg_index i ON i.indexrelid = s.indexrelid
		WHERE s.schemaname = $1 AND s.idx_scan = 0 AND NOT i.indisunique AND NOT i.indisprimary`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check unused indexes: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var indexName, table string
		var idxScan int64
		if err := rows.Scan(&indexName, &table, &idxScan); err != nil {
			return nil, fmt.Errorf("scan unused index: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "perf-0005",
			Severity:    "medium",
			Category:    "performance",
			Title:       "Unused index",
			Description: fmt.Sprintf("Index %q on table %q has never been scanned. It adds write overhead without read benefit.", indexName, table),
			Table:       table,
			Detail:      fmt.Sprintf("index=%s scans=%d", indexName, idxScan),
			Fix:         fmt.Sprintf(`DROP INDEX "%s"`, indexName),
		})
	}
	return findings, rows.Err()
}

// perf-0009: Duplicate Index
func (i *Introspector) checkDuplicateIndexes(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT a.indexrelname AS index_a, b.indexrelname AS index_b, a.relname AS table_name
		FROM pg_stat_user_indexes a
		JOIN pg_stat_user_indexes b ON a.relid = b.relid AND a.indexrelid != b.indexrelid
		JOIN pg_index ia ON ia.indexrelid = a.indexrelid
		JOIN pg_index ib ON ib.indexrelid = b.indexrelid
		WHERE a.schemaname = $1 AND ia.indkey = ib.indkey AND a.indexrelname < b.indexrelname`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check duplicate indexes: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var indexA, indexB, table string
		if err := rows.Scan(&indexA, &indexB, &table); err != nil {
			return nil, fmt.Errorf("scan duplicate index: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "perf-0009",
			Severity:    "medium",
			Category:    "performance",
			Title:       "Duplicate index",
			Description: fmt.Sprintf("Indexes %q and %q on table %q cover the same columns. Drop the less-used one to save space and write overhead.", indexA, indexB, table),
			Table:       table,
			Detail:      fmt.Sprintf("indexA=%s indexB=%s", indexA, indexB),
			Fix:         "Drop the less-used index.",
		})
	}
	return findings, rows.Err()
}

// --- Security rules ---

// sec-0006: Multiple Permissive Policies
func (i *Introspector) checkMultiplePermissivePolicies(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT tablename, COUNT(*) as policy_count
		FROM pg_policies
		WHERE schemaname = $1 AND permissive = 'PERMISSIVE'
		GROUP BY tablename
		HAVING COUNT(*) > 3`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check multiple permissive policies: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var table string
		var count int
		if err := rows.Scan(&table, &count); err != nil {
			return nil, fmt.Errorf("scan permissive policies: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0006",
			Severity:    "medium",
			Category:    "security",
			Title:       "Multiple permissive policies",
			Description: fmt.Sprintf("Table %q has %d permissive RLS policies. Permissive policies are OR'd together, which may grant broader access than intended.", table, count),
			Table:       table,
			Detail:      fmt.Sprintf("policy_count=%d", count),
		})
	}
	return findings, rows.Err()
}

// sec-0007: Policy Exists RLS Disabled
func (i *Introspector) checkPolicyExistsRLSDisabled(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT DISTINCT p.tablename
		FROM pg_policies p
		JOIN pg_class c ON c.relname = p.tablename
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = p.schemaname
		WHERE p.schemaname = $1 AND NOT c.relrowsecurity`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check policy exists RLS disabled: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan policy RLS disabled: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0007",
			Severity:    "critical",
			Category:    "security",
			Title:       "Policy exists but RLS is disabled",
			Description: fmt.Sprintf("Table %q has RLS policies but row-level security is not enabled. Policies are not enforced.", table),
			Table:       table,
			Fix:         fmt.Sprintf(`ALTER TABLE "%s" ENABLE ROW LEVEL SECURITY`, table),
		})
	}
	return findings, rows.Err()
}

// sec-0008: RLS Enabled No Policy
func (i *Introspector) checkRLSEnabledNoPolicy(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT c.relname AS table_name
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relrowsecurity
		AND NOT EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname = $1 AND p.tablename = c.relname)`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check RLS no policy: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan RLS no policy: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0008",
			Severity:    "high",
			Category:    "security",
			Title:       "RLS enabled without policies",
			Description: fmt.Sprintf("Table %q has row-level security enabled but no policies defined. Non-superusers will have no access.", table),
			Table:       table,
			Fix:         fmt.Sprintf(`CREATE POLICY "allow_all" ON "%s" USING (true)`, table),
		})
	}
	return findings, rows.Err()
}

// sec-0010: Security Definer View
func (i *Introspector) checkSecurityDefinerView(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		AND c.relkind = 'v'
		AND NOT coalesce(c.reloptions::text, '') LIKE '%security_invoker=on%'`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check security definer views: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var viewName string
		if err := rows.Scan(&viewName); err != nil {
			return nil, fmt.Errorf("scan security definer view: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0010",
			Severity:    "medium",
			Category:    "security",
			Title:       "Security definer view",
			Description: fmt.Sprintf("View %q does not use security_invoker. It runs with the privileges of the view creator, which may bypass RLS.", viewName),
			Table:       viewName,
			Fix:         fmt.Sprintf(`ALTER VIEW "%s" SET (security_invoker = on)`, viewName),
		})
	}
	return findings, rows.Err()
}

// sec-0011: Function Search Path Mutable
func (i *Introspector) checkFunctionSearchPathMutable(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT p.proname AS function_name
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proconfig IS NULL
		AND p.prosecdef = true`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check function search path: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var funcName string
		if err := rows.Scan(&funcName); err != nil {
			return nil, fmt.Errorf("scan function search path: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0011",
			Severity:    "high",
			Category:    "security",
			Title:       "Function search path mutable",
			Description: fmt.Sprintf("Function %q is SECURITY DEFINER but has no explicit search_path set. An attacker could hijack the search path.", funcName),
			Table:       funcName,
			Fix:         fmt.Sprintf(`ALTER FUNCTION "%s" SET search_path = public`, funcName),
		})
	}
	return findings, rows.Err()
}

// sec-0013: RLS Disabled in Public
func (i *Introspector) checkRLSDisabledInPublic(ctx context.Context, db *sql.DB, schema string) ([]AdvisorFinding, error) {
	const query = `
		SELECT c.relname AS table_name
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' AND NOT c.relrowsecurity`

	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("check RLS disabled: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan RLS disabled: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0013",
			Severity:    "medium",
			Category:    "security",
			Title:       "RLS disabled in public schema",
			Description: fmt.Sprintf("Table %q in schema %q does not have row-level security enabled.", table, schema),
			Table:       table,
			Fix:         fmt.Sprintf(`ALTER TABLE "%s" ENABLE ROW LEVEL SECURITY`, table),
		})
	}
	return findings, rows.Err()
}

// sec-0014: Extension in Public
func (i *Introspector) checkExtensionInPublic(ctx context.Context, db *sql.DB, _ string) ([]AdvisorFinding, error) {
	const query = `
		SELECT e.extname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE n.nspname = 'public' AND e.extname != 'plpgsql'`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("check extension in public: %w", err)
	}
	defer rows.Close()

	findings := make([]AdvisorFinding, 0)
	for rows.Next() {
		var extName string
		if err := rows.Scan(&extName); err != nil {
			return nil, fmt.Errorf("scan extension in public: %w", err)
		}
		findings = append(findings, AdvisorFinding{
			RuleID:      "sec-0014",
			Severity:    "low",
			Category:    "security",
			Title:       "Extension installed in public schema",
			Description: fmt.Sprintf("Extension %q is installed in the public schema. Consider moving it to a dedicated schema for better isolation.", extName),
			Detail:      fmt.Sprintf("extension=%s", extName),
		})
	}
	return findings, rows.Err()
}
