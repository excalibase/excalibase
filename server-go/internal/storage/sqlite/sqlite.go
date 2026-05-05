package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/golang-migrate/migrate/v4"
	migsqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite-backed storage for all domain entities.
type Store struct {
	db *sql.DB
	m  *migrate.Migrate
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite single writer
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying *sql.DB. Matches postgres.Store.DB(). Used by
// handlers that own bespoke tables (email_verifications, password_resets,
// email_invites) instead of going through InstanceStore/UserStore. Avoid
// for normal CRUD — that's what the typed methods are for.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	driver, err := migsqlite.WithInstance(s.db, &migsqlite.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("migration init: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration up: %w", err)
	}

	s.m = m
	return nil
}

func (s *Store) MigrationVersion() (uint, bool, error) {
	if s.m == nil {
		return 0, false, fmt.Errorf("migrations not initialized")
	}
	return s.m.Version()
}

// --- Shared helpers ---

func boolToInt(b *bool) int {
	if b != nil && *b {
		return 1
	}
	return 0
}

func intToBoolPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	b := n.Int64 == 1
	return &b
}

func flexTimeStr(ft *domain.FlexTime) *string {
	if ft == nil {
		return nil
	}
	s := ft.Time.Format(time.RFC3339Nano)
	return &s
}

func parseFlexTime(s string) *domain.FlexTime {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &domain.FlexTime{Time: t}
		}
	}
	return nil
}

func ptrStr(s *string) *string {
	return s
}
