package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/lib/pq"
)

// DefaultPublicationName is the production-default publication name.
// Watcher and graphql must agree on the same name; the value is
// configurable via env (REALTIME_PUBLICATION_NAME) at process startup.
const DefaultPublicationName = "cdc_watcher_pub"

// pubAlreadyMemberCode is the SQLSTATE postgres returns when ALTER
// PUBLICATION ADD TABLE is run for a table already in the publication.
// We treat it as success so toggle endpoints are idempotent.
const pubAlreadyMemberCode = "42710"

// pubNotMemberCode covers the inverse: DROP TABLE for a table that
// wasn't in the publication. Postgres uses 42704 (undefined object).
const pubNotMemberCode = "42704"

// TableState is one row of the bulk Realtime page.
type TableState struct {
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Enabled bool   `json:"enabled"`
}

// RealtimeService wraps publication-membership operations against a
// project's Postgres database. Constructed once per request from the
// per-tenant connection pool (excalibase_app credentials, fetched from
// vault). The service has no opinion on auth or routing — purely SQL.
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

// ListTables returns every user-data table with whether it's currently
// in the publication. System schemas (auth, pg_*, information_schema,
// the publication's own catalog) are excluded — the bulk page should
// never offer them as toggleable.
func (s *RealtimeService) ListTables(ctx context.Context) ([]TableState, error) {
	const q = `
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
		  AND n.nspname NOT IN ('auth', 'information_schema', 'pg_catalog', 'pg_toast')
		  AND n.nspname NOT LIKE 'pg_%'
		ORDER BY n.nspname, c.relname
	`
	rows, err := s.db.QueryContext(ctx, q, s.publicationName)
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

// EnableTable adds a table to the publication. Idempotent: ADD TABLE on
// a table that is already a member returns SQLSTATE 42710, which we
// swallow so callers don't have to special-case it.
func (s *RealtimeService) EnableTable(ctx context.Context, sch, tbl string) error {
	if !validIdent(sch) || !validIdent(tbl) {
		return fmt.Errorf("invalid identifier: %q.%q", sch, tbl)
	}
	q := fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s.%s",
		schema.QuoteIdent(s.publicationName),
		schema.QuoteIdent(sch),
		schema.QuoteIdent(tbl))
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		if isPgCode(err, pubAlreadyMemberCode) {
			return nil
		}
		return fmt.Errorf("enable %s.%s: %w", sch, tbl, err)
	}
	return nil
}

// DisableTable drops a table from the publication. Idempotent: DROP
// TABLE for a non-member returns SQLSTATE 42704; we swallow it.
func (s *RealtimeService) DisableTable(ctx context.Context, sch, tbl string) error {
	if !validIdent(sch) || !validIdent(tbl) {
		return fmt.Errorf("invalid identifier: %q.%q", sch, tbl)
	}
	q := fmt.Sprintf("ALTER PUBLICATION %s DROP TABLE %s.%s",
		schema.QuoteIdent(s.publicationName),
		schema.QuoteIdent(sch),
		schema.QuoteIdent(tbl))
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		if isPgCode(err, pubNotMemberCode) {
			return nil
		}
		return fmt.Errorf("disable %s.%s: %w", sch, tbl, err)
	}
	return nil
}

// EnableAll adds every currently-disabled user-data table to the
// publication in a single ALTER. Returns the number of tables added.
// Tables already in the publication are skipped (no error).
func (s *RealtimeService) EnableAll(ctx context.Context) (int, error) {
	tables, err := s.ListTables(ctx)
	if err != nil {
		return 0, err
	}
	var refs []string
	for _, t := range tables {
		if !t.Enabled {
			refs = append(refs, fmt.Sprintf("%s.%s",
				schema.QuoteIdent(t.Schema), schema.QuoteIdent(t.Table)))
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	q := fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s",
		schema.QuoteIdent(s.publicationName), strings.Join(refs, ", "))
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return 0, fmt.Errorf("bulk enable: %w", err)
	}
	return len(refs), nil
}

// DisableAll drops every currently-enabled table in a single ALTER.
// Returns the number of tables removed.
func (s *RealtimeService) DisableAll(ctx context.Context) (int, error) {
	tables, err := s.ListTables(ctx)
	if err != nil {
		return 0, err
	}
	var refs []string
	for _, t := range tables {
		if t.Enabled {
			refs = append(refs, fmt.Sprintf("%s.%s",
				schema.QuoteIdent(t.Schema), schema.QuoteIdent(t.Table)))
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	q := fmt.Sprintf("ALTER PUBLICATION %s DROP TABLE %s",
		schema.QuoteIdent(s.publicationName), strings.Join(refs, ", "))
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return 0, fmt.Errorf("bulk disable: %w", err)
	}
	return len(refs), nil
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

// isPgCode reports whether err carries the given Postgres SQLSTATE.
// Used to swallow idempotent re-add / re-drop attempts so callers
// don't have to reason about 42710 / 42704.
func isPgCode(err error, code string) bool {
	var pqErr *pq.Error
	if pqErr == nil {
		// Fall through to the unwrap path below.
	}
	for cur := err; cur != nil; {
		if e, ok := cur.(*pq.Error); ok {
			return string(e.Code) == code
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return false
}
