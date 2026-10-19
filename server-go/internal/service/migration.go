package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

type MigrationService struct {
	store       storage.InstanceStore
	vault       vaultclient.VaultClient
	storagePath string
	overrides   dsnOverrides
	now         func() time.Time
}

// dsnOverrides lets local dev point the tenant connection at a port-forward
// (SCHEMA_DB_HOST/PORT/SSLMODE), mirroring the schema handler.
type dsnOverrides struct {
	host    string
	port    string
	sslmode string
}

func NewMigrationService(store storage.InstanceStore, vault vaultclient.VaultClient, storagePath string) *MigrationService {
	return &MigrationService{
		store:       store,
		vault:       vault,
		storagePath: storagePath,
		now:         time.Now,
		overrides: dsnOverrides{
			host:    os.Getenv("SCHEMA_DB_HOST"),
			port:    os.Getenv("SCHEMA_DB_PORT"),
			sslmode: os.Getenv("SCHEMA_DB_SSLMODE"),
		},
	}
}

// buildTenantDSN composes a lib/pq connection string from vault credentials.
// An explicit host override (local dev port-forward) flips sslmode to disable
// unless a mode is given.
func buildTenantDSN(creds map[string]string, o dsnOverrides) (string, error) {
	return projectdb.DSNFor(creds, projectdb.Overrides{Host: o.host, Port: o.port, SSLMode: o.sslmode})
}

// openTenantDB opens a connection to the project's database as the non-superuser
// excalibase_app role, using credentials from vault. This is the same role the
// schema editor and data plane use — it holds CREATE on the database (so DDL
// migrations work) but is NOT a superuser, so COPY ... TO PROGRAM and other
// OS-level escapes are rejected by Postgres (SEC-C2).
func (s *MigrationService) openTenantDB(projectID string) (*sql.DB, error) {
	creds, err := s.vault.Get(fmt.Sprintf("projects/%s/credentials/excalibase_app", projectID))
	if err != nil {
		return nil, fmt.Errorf("read excalibase_app credentials: %w", err)
	}
	dsn, err := buildTenantDSN(creds, s.overrides)
	if err != nil {
		return nil, err
	}
	// No statement timeout bounds a migration, so its read bound is long.
	db, err := projectdb.OpenPostgres(dsn, projectdb.MigrationIOTimeout)
	if err != nil {
		return nil, fmt.Errorf("open tenant connection: %w", err)
	}
	return db, nil
}

func (s *MigrationService) ApplyMigration(ctx context.Context, projectID string, req domain.MigrationRequest) (*domain.MigrationRecord, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	db, err := s.openTenantDB(projectID)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	now := s.now()

	start := time.Now()
	// Runs as excalibase_app (non-superuser). lib/pq sends the whole SQL as a
	// simple query, so multi-statement migrations execute together.
	_, execErr := db.ExecContext(ctx, req.SQL)
	elapsed := time.Since(start).Milliseconds()

	// Display-only fingerprint (truncated SHA-256), not a security control.
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(req.SQL)))[:8]

	record := &domain.MigrationRecord{
		ProjectID:       projectID,
		Version:         req.Version,
		Name:            req.Name,
		Description:     req.Description,
		SQL:             req.SQL,
		ExecutionTimeMs: elapsed,
		Checksum:        checksum,
	}

	ft := &domain.UTCTime{Time: now}
	if execErr != nil {
		record.Status = "FAILED"
		record.ErrorMessage = execErr.Error()
		record.Output = execErr.Error()
	} else {
		record.Status = "APPLIED"
		record.Output = "ok"
		record.AppliedAt = ft
	}

	dir := filepath.Join(s.storagePath, "projects", projectID, "migrations")
	if err := saveMigrationRecord(dir, now, record); err != nil {
		log.Printf("WARN: migration record for %s not saved: %v", projectID, err)
	}

	return record, nil
}

// maxIDAttempts bounds the search for a free id among migrations recorded in the same nanosecond.
const maxIDAttempts = 1000

// saveMigrationRecord gives record the first free id at or after now and writes it without ever
// replacing another record. Ids are mig-<yyyymmdd-hhmmss>-<nanoseconds, 9 digits>, so file-name order is
// application order, and second-precision ids written before EXC-605 still sort by their time.
func saveMigrationRecord(dir string, now time.Time, record *domain.MigrationRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	stamp := now.Format("20060102-150405")
	nanos := now.Nanosecond()
	for attempt := 0; attempt < maxIDAttempts; attempt++ {
		record.ID = fmt.Sprintf("mig-%s-%09d", stamp, nanos+attempt)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", record.ID, err)
		}
		file, err := os.OpenFile(filepath.Join(dir, record.ID+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create %s: %w", record.ID, err)
		}
		_, writeErr := file.Write(data)
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return fmt.Errorf("write %s: %w", record.ID, writeErr)
		}
		return nil
	}
	return fmt.Errorf("no free migration id at %s", stamp)
}

func (s *MigrationService) ListMigrations(projectID string) ([]domain.MigrationRecord, error) {
	dir := filepath.Join(s.storagePath, "projects", projectID, "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []domain.MigrationRecord{}, nil
	}

	result := make([]domain.MigrationRecord, 0)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		if err != nil {
			return nil, fmt.Errorf("read migration record %s: %w", e.Name(), err)
		}
		var rec domain.MigrationRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			log.Printf("WARN: migration record %s/%s is unreadable and is not listed: %v", projectID, e.Name(), err)
			continue
		}
		result = append(result, rec)
	}
	return result, nil
}
