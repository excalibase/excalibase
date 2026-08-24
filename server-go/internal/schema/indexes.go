package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// allowedIndexTypes is the whitelist of valid PostgreSQL index types.
var allowedIndexTypes = map[string]bool{
	"btree": true,
	"hash":  true,
	"gin":   true,
	"gist":  true,
	"brin":  true,
}

// ValidateIndexType validates that the index type is in the allowlist.
func ValidateIndexType(indexType string) error {
	if !allowedIndexTypes[strings.ToLower(indexType)] {
		return fmt.Errorf("invalid index type: %q (must be btree, hash, gin, gist, or brin)", indexType)
	}
	return nil
}

// CreateIndex creates a new index on a table.
func (i *Introspector) CreateIndex(ctx context.Context, db *sql.DB, req CreateIndexRequest) error {
	schemaName := req.Schema
	if schemaName == "" {
		schemaName = "public"
	}

	if len(req.Columns) == 0 {
		return fmt.Errorf("at least one column is required")
	}

	indexType := strings.ToLower(req.Type)
	if indexType == "" {
		indexType = "btree"
	}
	if err := ValidateIndexType(indexType); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("CREATE ")
	if req.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	b.WriteString(QuoteIdent(req.Name))
	b.WriteString(" ON ")
	b.WriteString(QuoteIdent(schemaName))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Table))
	b.WriteString(" USING ")
	b.WriteString(indexType)
	b.WriteString(" (")

	quotedCols := make([]string, 0, len(req.Columns))
	for _, col := range req.Columns {
		quotedCols = append(quotedCols, QuoteIdent(col))
	}
	b.WriteString(strings.Join(quotedCols, ", "))
	b.WriteString(")")

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// DropIndex drops an index by name.
func (i *Introspector) DropIndex(ctx context.Context, db *sql.DB, schema, name string) error {
	stmt := "DROP INDEX " + QuoteIdent(schema) + "." + QuoteIdent(name)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop index: %w", err)
	}
	return nil
}
