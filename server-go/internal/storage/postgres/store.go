package postgres

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/golang-migrate/migrate/v4"
	migpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the PostgreSQL-backed storage for all domain entities.
type Store struct {
	db *sql.DB
	m  *migrate.Migrate
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

func New(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for shared use (e.g., vault store).
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	driver, err := migpg.WithInstance(s.db, &migpg.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
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

func boolPtr(b bool) *bool {
	return &b
}

func derefBool(b *bool) bool {
	if b != nil {
		return *b
	}
	return false
}

func flexTimePtr(ft *domain.FlexTime) *time.Time {
	if ft == nil {
		return nil
	}
	return &ft.Time
}

// toFlexTime is the inverse of flexTimePtr. Used by integration tests —
// `go vet` without `-tags=integration` won't see those callers and may
// flag this as unused.
func toFlexTime(t *time.Time) *domain.FlexTime {
	if t == nil {
		return nil
	}
	return &domain.FlexTime{Time: *t}
}


func ptrStr(s *string) *string {
	return s
}
