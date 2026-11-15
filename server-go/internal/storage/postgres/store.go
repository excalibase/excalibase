package postgres

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
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
	// schemaVersion is what the migrator left the schema at; the migrator
	// itself is closed once it has run (see migrate).
	schemaVersion    uint
	schemaVersionSet bool
	databaseURL      string
	leaders          *LeaderSession
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

// New opens the platform store with the default pool size. Callers that read
// configuration use NewWithMaxConns.
func New(databaseURL string) (*Store, error) {
	return NewWithMaxConns(databaseURL, config.DefaultPlatformDBMaxConns)
}

// NewWithMaxConns opens the platform store with an explicit pool size. The
// value is validated where it is read (internal/config), so an unusable one
// never reaches here.
func NewWithMaxConns(databaseURL string, maxOpen int) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	// Pool arithmetic. All standing leadership claims share one pinned
	// session (LeaderSession), and every in-flight lifecycle operation pins
	// one more for its duration — a pause can run for minutes. Sized by PLATFORM_DB_MAX_CONNS; see
	// config.MinPlatformDBMaxConns and OPERATOR.md.
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen / 2)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &Store{db: db, databaseURL: databaseURL, leaders: NewLeaderSession(db)}
	if err := store.migrate(databaseURL); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

// LeaderLock is the standing leadership claim for key. All such claims in the
// process share one database session, so their number never competes with
// requests for pool connections.
func (s *Store) LeaderLock(key int64) storage.LeaderLock {
	return s.leaders.Lock(key)
}

func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for shared use (e.g., vault store).
func (s *Store) DB() *sql.DB {
	return s.db
}

// newMigrator builds the embedded-migrations runner on a pool of its own.
// golang-migrate pins one connection for as long as its driver lives, so
// running it on the shared pool left that pool one connection short for the
// life of the process; a throwaway pool is closed with the migrator
// (migrate.Close) and takes the pin with it.
func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migration source: %w", err)
	}
	migrationDB, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("migration pool: %w", err)
	}
	driver, err := migpg.WithInstance(migrationDB, &migpg.Config{})
	if err != nil {
		_ = migrationDB.Close()
		return nil, fmt.Errorf("migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		_ = driver.Close()
		return nil, fmt.Errorf("migration init: %w", err)
	}
	return m, nil
}

func (s *Store) migrate(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration up: %w", err)
	}
	if version, dirty, err := m.Version(); err == nil && !dirty {
		s.schemaVersion, s.schemaVersionSet = version, true
	}
	return nil
}

func (s *Store) MigrationVersion() (uint, bool, error) {
	if !s.schemaVersionSet {
		return 0, false, fmt.Errorf("migrations not initialized")
	}
	return s.schemaVersion, false, nil
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
