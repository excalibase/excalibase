package edgefn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
)

// nosqlSchemaName is the postgres schema under which the NoSQL tables live.
// Mirrors the convention used by the Java NoSQL service: one schema per
// project's nosql storage, addressable as nosql.<table>.
const nosqlSchemaName = "nosql"

// safeIdentPattern guards every table/index/field name the migrator interpolates
// into DDL. We accept only the safe ASCII identifier subset; anything else is
// rejected before reaching SQL. This is defence-in-depth — schema names already
// come from validated TypeScript identifiers, but we re-check before composing
// CREATE TABLE / CREATE INDEX strings.
var safeIdentPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// validateIdent panics — sorry, returns an error — when an identifier fails
// the safety check. Caller propagates so the deploy fails atomically.
func validateIdent(kind, ident string) error {
	if !safeIdentPattern.MatchString(ident) {
		return fmt.Errorf("invalid %s identifier %q: must match %s", kind, ident, safeIdentPattern.String())
	}
	return nil
}

// quoteIdent wraps a validated identifier in double quotes. After
// validateIdent succeeds, the identifier contains no characters that would
// require escaping, so this is safe.
func quoteIdent(ident string) string {
	return `"` + ident + `"`
}

// ApplySchema translates the provided Schema into a sequence of additive DDL
// statements and runs them in a single transaction. Idempotent — every
// statement uses IF NOT EXISTS so re-application is a no-op.
//
// Constraints (v1):
//   - Additive only. Indexes and columns added in a later schema show up;
//     indexes and columns removed are silently retained with a warning log.
//   - Never DROPs tables, columns, or indexes.
//   - Every identifier is validated against safeIdentPattern before
//     interpolation.
//   - All DDL runs inside `nosql` schema; the caller is expected to have
//     created the schema with the project-scoped DB role.
//
// projectID is logged but does NOT prefix the table name — projects already
// have their own database, so collisions within nosql.* would only happen
// across different schemas the user themselves declared.
//
// Phase 8: ApplySchema also creates the Phase 8 scheduler/cron tables
// (`excalibase_scheduled_functions`, `excalibase_cron_jobs`) on every
// deploy. These tables live at the public schema (not under `nosql.*`)
// because they are platform metadata, not user data. The DDL is
// idempotent so re-application is free.
func ApplySchema(ctx context.Context, db *sql.DB, projectID string, schema Schema) error {
	// Short-circuit on empty schema BEFORE any DB access so callers (and
	// unit tests) can pass a nil DB when there's nothing to migrate.
	if len(schema.Tables) == 0 {
		return nil
	}
	// Phase 8: ensure scheduler tables exist on every real migration.
	// Safe to run before the user-schema DDL because it doesn't touch
	// the `nosql.*` schema or any user tables.
	if err := ensureSchedulerTables(ctx, db); err != nil {
		return fmt.Errorf("ensure scheduler tables: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		// If we return without committing, rollback. tx.Rollback after a
		// successful Commit returns sql.ErrTxDone which we ignore.
		_ = tx.Rollback()
	}()

	// Ensure schema exists (idempotent). Migration is scoped to nosql.*.
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdent(nosqlSchemaName)); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Sort table names so the DDL order is deterministic — easier to read
	// in logs and gives stable test output.
	tableNames := make([]string, 0, len(schema.Tables))
	for n := range schema.Tables {
		tableNames = append(tableNames, n)
	}
	sort.Strings(tableNames)

	for _, tableName := range tableNames {
		table := schema.Tables[tableName]
		if err := validateIdent("table", tableName); err != nil {
			return err
		}
		if err := applyTable(ctx, tx, tableName, table); err != nil {
			return fmt.Errorf("apply table %s: %w", tableName, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Printf("schema applied: project=%s tables=%d", projectID, len(tableNames))
	return nil
}

func applyTable(ctx context.Context, tx *sql.Tx, tableName string, table TableSchema) error {
	// 1. CREATE TABLE IF NOT EXISTS — base table with system fields. User
	//    fields are persisted in a single jsonb document column to match the
	//    existing NoSQL service convention. Field-level checks live in the
	//    application layer (the validator); the DB stores the blob.
	createSQL := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s.%s (
			_id text PRIMARY KEY,
			_creation_time double precision NOT NULL,
			doc jsonb NOT NULL DEFAULT '{}'::jsonb
		)`,
		quoteIdent(nosqlSchemaName), quoteIdent(tableName),
	)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// 2. Compound btree indexes — one per index spec.
	if err := applyBtreeIndexes(ctx, tx, tableName, table.Indexes); err != nil {
		return err
	}

	// 3. Search indexes (tsvector).
	if err := applySearchIndexes(ctx, tx, tableName, table.SearchIndexes); err != nil {
		return err
	}

	// 4. Vector indexes (pgvector ivfflat).
	if err := applyVectorIndexes(ctx, tx, tableName, table.VectorIndexes); err != nil {
		return err
	}

	// Validator metadata is intentionally NOT applied as a DB constraint in
	// v1. The application layer validates writes against the JSON Schema;
	// keeping the constraint out of Postgres lets us evolve the validator
	// shape without ALTER TABLE migrations on every push. We stash the raw
	// validator JSON for future referential checks.
	rawValidator, err := json.Marshal(table.Validator)
	if err != nil {
		return fmt.Errorf("marshal validator metadata: %w", err)
	}
	// Use a COMMENT to attach the schema so it survives across deploys
	// without an extra metadata table. pg_description holds it.
	commentSQL := fmt.Sprintf(
		`COMMENT ON TABLE %s.%s IS %s`,
		quoteIdent(nosqlSchemaName), quoteIdent(tableName),
		quoteLiteral(string(rawValidator)),
	)
	if _, err := tx.ExecContext(ctx, commentSQL); err != nil {
		return fmt.Errorf("comment validator: %w", err)
	}
	return nil
}

// applyBtreeIndexes creates one compound btree index per spec. Each indexes
// the extracted jsonb path so queries can use `doc->>'field'`.
func applyBtreeIndexes(ctx context.Context, tx *sql.Tx, tableName string, indexes []IndexSpec) error {
	for _, idx := range indexes {
		if err := validateIdent("index", idx.Name); err != nil {
			return err
		}
		if len(idx.Fields) == 0 {
			return fmt.Errorf("index %s.%s has no fields", tableName, idx.Name)
		}
		exprs := make([]string, 0, len(idx.Fields))
		for _, f := range idx.Fields {
			if err := validateIdent("field", f); err != nil {
				return err
			}
			exprs = append(exprs, fmt.Sprintf("(doc ->> '%s')", f))
		}
		ddl := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s.%s (%s)`,
			quoteIdent(tableName+"_"+idx.Name),
			quoteIdent(nosqlSchemaName), quoteIdent(tableName),
			strings.Join(exprs, ", "),
		)
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create index %s: %w", idx.Name, err)
		}
	}
	return nil
}

// applySearchIndexes creates a stored tsvector generated column per searchField
// plus a GIN index on it. Idempotent via IF NOT EXISTS on both.
func applySearchIndexes(ctx context.Context, tx *sql.Tx, tableName string, indexes []SearchIndexSpec) error {
	for _, idx := range indexes {
		if err := validateIdent("search index", idx.Name); err != nil {
			return err
		}
		if err := validateIdent("field", idx.SearchField); err != nil {
			return err
		}
		colName := "search_vector_" + idx.SearchField
		if err := validateIdent("search column", colName); err != nil {
			return err
		}
		// We use a stored generated column (ADD COLUMN IF NOT EXISTS) — older
		// Postgres versions (>=12) support this and the column gets recomputed
		// on every row write.
		addCol := fmt.Sprintf(
			`ALTER TABLE %s.%s ADD COLUMN IF NOT EXISTS %s tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(doc ->> '%s', ''))) STORED`,
			quoteIdent(nosqlSchemaName), quoteIdent(tableName),
			quoteIdent(colName), idx.SearchField,
		)
		if _, err := tx.ExecContext(ctx, addCol); err != nil {
			return fmt.Errorf("add search column %s: %w", colName, err)
		}
		ddl := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s.%s USING GIN (%s)`,
			quoteIdent(tableName+"_search_"+idx.Name),
			quoteIdent(nosqlSchemaName), quoteIdent(tableName),
			quoteIdent(colName),
		)
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create search index %s: %w", idx.Name, err)
		}
	}
	return nil
}

// applyVectorIndexes creates a pgvector embedding column at the declared
// dimensions plus an ivfflat cosine index per spec. The application writes the
// embedding directly to this column; the jsonb doc holds a copy too.
func applyVectorIndexes(ctx context.Context, tx *sql.Tx, tableName string, indexes []VectorIndexSpec) error {
	for _, idx := range indexes {
		if err := validateIdent("vector index", idx.Name); err != nil {
			return err
		}
		if err := validateIdent("field", idx.VectorField); err != nil {
			return err
		}
		if idx.Dimensions <= 0 || idx.Dimensions > 16000 {
			return fmt.Errorf("vector index %s: invalid dimensions %d (must be 1-16000)", idx.Name, idx.Dimensions)
		}
		colName := "embedding_" + idx.VectorField
		if err := validateIdent("vector column", colName); err != nil {
			return err
		}
		addCol := fmt.Sprintf(
			`ALTER TABLE %s.%s ADD COLUMN IF NOT EXISTS %s vector(%d)`,
			quoteIdent(nosqlSchemaName), quoteIdent(tableName),
			quoteIdent(colName), idx.Dimensions,
		)
		if _, err := tx.ExecContext(ctx, addCol); err != nil {
			return fmt.Errorf("add vector column %s: %w", colName, err)
		}
		ddl := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s.%s USING ivfflat (%s vector_cosine_ops)`,
			quoteIdent(tableName+"_vector_"+idx.Name),
			quoteIdent(nosqlSchemaName), quoteIdent(tableName),
			quoteIdent(colName),
		)
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create vector index %s: %w", idx.Name, err)
		}
	}
	return nil
}

// quoteLiteral SQL-quotes a string literal using doubled single quotes.
// The migrator only writes its own JSON output here, so the input is
// already controlled — this is defence-in-depth.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ensureSchedulerTables creates the Phase 8 scheduler bookkeeping tables
// on the project database. Idempotent: every CREATE uses IF NOT EXISTS
// so re-application during normal deploys is a no-op.
//
// Mirrors `scheduler.EnsureTables` — kept duplicated here (rather than
// imported) so the migrator package stays free of cross-package
// dependencies on the runtime worker.
func ensureSchedulerTables(ctx context.Context, db *sql.DB) error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS excalibase_scheduled_functions (
			id text PRIMARY KEY,
			project_id text NOT NULL,
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			scheduled_for timestamptz NOT NULL,
			status text NOT NULL DEFAULT 'pending',
			attempts int NOT NULL DEFAULT 0,
			last_error text,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS excalibase_scheduled_functions_due_idx
			ON excalibase_scheduled_functions (status, scheduled_for)
			WHERE status = 'pending';
		CREATE TABLE IF NOT EXISTS excalibase_cron_jobs (
			name text NOT NULL,
			project_id text NOT NULL,
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			schedule jsonb NOT NULL,
			last_enqueued_at timestamptz,
			PRIMARY KEY (project_id, name)
		);
	`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return err
	}
	return nil
}
