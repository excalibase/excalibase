package service

import (
	"context"
	"database/sql"
	"fmt"
)

// DefaultPublicationName is the production-default publication name.
// Watcher and graphql must agree on the same name; the value is
// configurable via env (REALTIME_PUBLICATION_NAME) at process startup.
const DefaultPublicationName = "cdc_watcher_pub"

// realtimeUserSchemaPredicate decides which schemas hold user tables that may
// be published, over pg_namespace aliased n. The listing spells it out as a
// literal (a test keeps them equal) so the page never offers a table the
// provisioning-time function refuses.
const realtimeUserSchemaPredicate = `n.nspname NOT IN ('auth', 'excalibase', 'information_schema')
    AND NOT starts_with(n.nspname, 'pg_')
    AND NOT starts_with(n.nspname, 'excalibase_')`

const setRealtimeTableSQL = `SELECT excalibase.set_realtime_table($1, $2, $3)`

// TableState is one row of the bulk Realtime page.
type TableState struct {
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Enabled bool   `json:"enabled"`
}

// RealtimeService wraps publication-membership operations against a
// project's Postgres database, connected as excalibase_app. Membership
// changes go through excalibase.set_realtime_table, created at provisioning.
type RealtimeService struct {
	db              *sql.DB
	publicationName string
}

// NewRealtimeService constructs the service with the production-default
// publication name. Use NewRealtimeServiceWithName for custom deploys
// (e.g. e2e tests where the watcher uses a different publication).
func NewRealtimeService(db *sql.DB) *RealtimeService {
	return &RealtimeService{db: db, publicationName: DefaultPublicationName}
}

func NewRealtimeServiceWithName(db *sql.DB, publicationName string) *RealtimeService {
	if publicationName == "" {
		publicationName = DefaultPublicationName
	}
	return &RealtimeService{db: db, publicationName: publicationName}
}

const listRealtimeTablesSQL = `
		SELECT
			n.nspname AS schema,
			c.relname AS table_name,
			EXISTS (
				SELECT 1 FROM pg_publication_tables pt
				WHERE pt.pubname = $1
				  AND pt.schemaname = n.nspname
				  AND pt.tablename = c.relname
			) AS enabled
		FROM pg_class c
		JOIN pg_namespace n ON c.relnamespace = n.oid
		WHERE c.relkind = 'r'
		  AND n.nspname NOT IN ('auth', 'excalibase', 'information_schema')
    AND NOT starts_with(n.nspname, 'pg_')
    AND NOT starts_with(n.nspname, 'excalibase_')
		ORDER BY n.nspname, c.relname
	`

// ListTables returns every user-data table with whether it's currently
// in the publication. Auth, platform and system schemas are excluded.
func (s *RealtimeService) ListTables(ctx context.Context) ([]TableState, error) {
	rows, err := s.db.QueryContext(ctx, listRealtimeTablesSQL, s.publicationName)
	if err != nil {
		return nil, fmt.Errorf("list publication tables: %w", err)
	}
	defer rows.Close()

	var out []TableState
	for rows.Next() {
		var t TableState
		if err := rows.Scan(&t.Schema, &t.Table, &t.Enabled); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EnableTable adds a table to the publication. Idempotent.
func (s *RealtimeService) EnableTable(ctx context.Context, sch, tbl string) error {
	return s.setTable(ctx, sch, tbl, true)
}

// DisableTable drops a table from the publication. Idempotent.
func (s *RealtimeService) DisableTable(ctx context.Context, sch, tbl string) error {
	return s.setTable(ctx, sch, tbl, false)
}

func (s *RealtimeService) setTable(ctx context.Context, sch, tbl string, publish bool) error {
	if !validIdent(sch) || !validIdent(tbl) {
		return fmt.Errorf("invalid identifier: %q.%q", sch, tbl)
	}
	if _, err := s.db.ExecContext(ctx, setRealtimeTableSQL, sch, tbl, publish); err != nil {
		return fmt.Errorf("set realtime %s.%s: %w", sch, tbl, err)
	}
	return nil
}

// EnableAll adds every currently-disabled user-data table to the
// publication in one transaction. Returns the number of tables added.
func (s *RealtimeService) EnableAll(ctx context.Context) (int, error) {
	return s.setAll(ctx, true)
}

// DisableAll drops every currently-enabled table in one transaction.
// Returns the number of tables removed.
func (s *RealtimeService) DisableAll(ctx context.Context) (int, error) {
	return s.setAll(ctx, false)
}

func (s *RealtimeService) setAll(ctx context.Context, publish bool) (int, error) {
	tables, err := s.ListTables(ctx)
	if err != nil {
		return 0, err
	}
	var pending []TableState
	for _, t := range tables {
		if t.Enabled != publish {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	for _, t := range pending {
		if _, err := tx.ExecContext(ctx, setRealtimeTableSQL, t.Schema, t.Table, publish); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("set realtime %s.%s: %w", t.Schema, t.Table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(pending), nil
}

// validIdent guards against SQL-injectable schema/table names. The
// value still gets QuoteIdent'd downstream, but rejecting illegal
// shapes upfront yields a clearer 4xx than letting Postgres parse-fail.
func validIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
