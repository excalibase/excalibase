package edgefn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/logsafe"
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
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// createNosqlSchemaSQL is a literal; a test pins it to nosqlSchemaName.
const createNosqlSchemaSQL = `CREATE SCHEMA IF NOT EXISTS "nosql"`

const createIndexIfMissing = "CREATE INDEX IF NOT EXISTS "

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
// The scheduler/cron tables are not created here: db is the app role, which
// customer SQL also runs as, so it may not create in the reserved schema.
// Registration creates them (EXC-623).
func ApplySchema(ctx context.Context, db *sql.DB, projectID string, schema Schema) error {
	// Short-circuit on empty schema BEFORE any DB access so callers (and
	// unit tests) can pass a nil DB when there's nothing to migrate.
	if len(schema.Tables) == 0 {
		return nil
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
	if _, err := tx.ExecContext(ctx, createNosqlSchemaSQL); err != nil {
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
	log.Printf("schema applied: project=%s tables=%d", logsafe.Value(projectID), len(tableNames))
	return nil
}

func applyTable(ctx context.Context, tx *sql.Tx, tableName string, table TableSchema) error {
	// 1. CREATE TABLE IF NOT EXISTS — base table with system fields. User
	//    fields are persisted in a single jsonb document column to match the
	//    existing NoSQL service convention. Field-level checks live in the
	//    application layer (the validator); the DB stores the blob.
	if _, err := tx.ExecContext(ctx, buildNosqlCreateTableSQL(tableName)); err != nil { // NOSONAR: tableName passed validateIdent and is quoted; DDL cannot bind identifiers
		return fmt.Errorf("create table: %w", err)
	}

	// 2. Compound btree indexes — one per index spec.
	for _, idx := range table.Indexes {
		step, err := buildBtreeIndexSQL(tableName, idx)
		if err != nil {
			return err
		}
		if err := execMigrationSteps(ctx, tx, step); err != nil {
			return err
		}
	}

	// 3. Search indexes (tsvector).
	for _, idx := range table.SearchIndexes {
		steps, err := buildSearchIndexSQL(tableName, idx)
		if err != nil {
			return err
		}
		if err := execMigrationSteps(ctx, tx, steps...); err != nil {
			return err
		}
	}

	// 4. Vector indexes (pgvector ivfflat).
	for _, idx := range table.VectorIndexes {
		steps, err := buildVectorIndexSQL(tableName, idx)
		if err != nil {
			return err
		}
		if err := execMigrationSteps(ctx, tx, steps...); err != nil {
			return err
		}
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
	if _, err := tx.ExecContext(ctx, buildValidatorCommentSQL(tableName, string(rawValidator))); err != nil { // NOSONAR: tableName passed validateIdent and is quoted, the validator is a quoted literal
		return fmt.Errorf("comment validator: %w", err)
	}
	return nil
}

// migrationStep is one DDL statement and the label its failure is reported under.
type migrationStep struct {
	sql  string
	what string
}

func execMigrationSteps(ctx context.Context, tx *sql.Tx, steps ...migrationStep) error {
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.sql); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}
	return nil
}

// nosqlTable is the quoted, schema-qualified name of a nosql table.
func nosqlTable(tableName string) string {
	return quoteIdent(nosqlSchemaName) + "." + quoteIdent(tableName)
}

// docPath is the jsonb text extraction of a document field, the field a quoted literal.
func docPath(field string) string {
	return "doc ->> " + quoteLiteral(field)
}

func buildNosqlCreateTableSQL(tableName string) string {
	return "CREATE TABLE IF NOT EXISTS " + nosqlTable(tableName) + ` (
			_id text PRIMARY KEY,
			_creation_time double precision NOT NULL,
			doc jsonb NOT NULL DEFAULT '{}'::jsonb
		)`
}

func buildValidatorCommentSQL(tableName, rawValidator string) string {
	return "COMMENT ON TABLE " + nosqlTable(tableName) + " IS " + quoteLiteral(rawValidator)
}

// buildBtreeIndexSQL builds one compound btree index over the extracted jsonb
// paths so queries can use `doc->>'field'`.
func buildBtreeIndexSQL(tableName string, idx IndexSpec) (migrationStep, error) {
	if err := validateIdent("index", idx.Name); err != nil {
		return migrationStep{}, err
	}
	if len(idx.Fields) == 0 {
		return migrationStep{}, fmt.Errorf("index %s.%s has no fields", tableName, idx.Name)
	}
	exprs := make([]string, 0, len(idx.Fields))
	for _, f := range idx.Fields {
		if err := validateIdent("field", f); err != nil {
			return migrationStep{}, err
		}
		exprs = append(exprs, "("+docPath(f)+")")
	}
	return migrationStep{
		sql: createIndexIfMissing + quoteIdent(tableName+"_"+idx.Name) +
			" ON " + nosqlTable(tableName) + " (" + strings.Join(exprs, ", ") + ")",
		what: "create index " + idx.Name,
	}, nil
}

// buildSearchIndexSQL builds a stored tsvector generated column for the
// search field plus a GIN index on it. Idempotent via IF NOT EXISTS on both.
func buildSearchIndexSQL(tableName string, idx SearchIndexSpec) ([]migrationStep, error) {
	if err := validateIdent("search index", idx.Name); err != nil {
		return nil, err
	}
	if err := validateIdent("field", idx.SearchField); err != nil {
		return nil, err
	}
	colName := "search_vector_" + idx.SearchField
	if err := validateIdent("search column", colName); err != nil {
		return nil, err
	}
	// We use a stored generated column (ADD COLUMN IF NOT EXISTS) — older
	// Postgres versions (>=12) support this and the column gets recomputed
	// on every row write.
	return []migrationStep{
		{
			sql: "ALTER TABLE " + nosqlTable(tableName) + " ADD COLUMN IF NOT EXISTS " + quoteIdent(colName) +
				" tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(" + docPath(idx.SearchField) + ", ''))) STORED",
			what: "add search column " + colName,
		},
		{
			sql: createIndexIfMissing + quoteIdent(tableName+"_search_"+idx.Name) +
				" ON " + nosqlTable(tableName) + " USING GIN (" + quoteIdent(colName) + ")",
			what: "create search index " + idx.Name,
		},
	}, nil
}

// buildVectorIndexSQL builds a pgvector embedding column at the declared
// dimensions plus an ivfflat cosine index. The application writes the
// embedding directly to this column; the jsonb doc holds a copy too.
func buildVectorIndexSQL(tableName string, idx VectorIndexSpec) ([]migrationStep, error) {
	if err := validateIdent("vector index", idx.Name); err != nil {
		return nil, err
	}
	if err := validateIdent("field", idx.VectorField); err != nil {
		return nil, err
	}
	if idx.Dimensions <= 0 || idx.Dimensions > 16000 {
		return nil, fmt.Errorf("vector index %s: invalid dimensions %d (must be 1-16000)", idx.Name, idx.Dimensions)
	}
	colName := "embedding_" + idx.VectorField
	if err := validateIdent("vector column", colName); err != nil {
		return nil, err
	}
	return []migrationStep{
		{
			sql: "ALTER TABLE " + nosqlTable(tableName) + " ADD COLUMN IF NOT EXISTS " + quoteIdent(colName) +
				" vector(" + strconv.Itoa(idx.Dimensions) + ")",
			what: "add vector column " + colName,
		},
		{
			sql: createIndexIfMissing + quoteIdent(tableName+"_vector_"+idx.Name) +
				" ON " + nosqlTable(tableName) + " USING ivfflat (" + quoteIdent(colName) + " vector_cosine_ops)",
			what: "create vector index " + idx.Name,
		},
	}, nil
}

// quoteLiteral SQL-quotes a string literal using doubled single quotes.
// The migrator only writes its own JSON output here, so the input is
// already controlled — this is defence-in-depth.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
