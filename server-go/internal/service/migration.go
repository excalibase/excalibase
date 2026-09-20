package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
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
	db, err := sql.Open("postgres", dsn)
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

	now := time.Now()
	migID := fmt.Sprintf("mig-%s", now.Format("20060102-150405"))

	start := time.Now()
	// Runs as excalibase_app (non-superuser). lib/pq sends the whole SQL as a
	// simple query, so multi-statement migrations execute together.
	_, execErr := db.ExecContext(ctx, req.SQL)
	elapsed := time.Since(start).Milliseconds()

	// Display-only fingerprint (truncated SHA-256), not a security control.
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(req.SQL)))[:8]

	record := &domain.MigrationRecord{
		ID:              migID,
		ProjectID:       projectID,
		Version:         req.Version,
		Name:            req.Name,
		Description:     req.Description,
		SQL:             req.SQL,
		ExecutionTimeMs: elapsed,
		Checksum:        checksum,
	}

	ft := &domain.FlexTime{Time: now}
	if execErr != nil {
		record.Status = "FAILED"
		record.ErrorMessage = execErr.Error()
		record.Output = execErr.Error()
	} else {
		record.Status = "APPLIED"
		record.Output = "ok"
		record.AppliedAt = ft
	}

	// Save to disk
	dir := filepath.Join(s.storagePath, "projects", projectID, "migrations")
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		log.Printf("WARN: mkdir %s: %v", dir, mkErr)
	}
	data, marshalErr := json.MarshalIndent(record, "", "  ")
	if marshalErr != nil {
		log.Printf("WARN: marshal migration record %s: %v", migID, marshalErr)
	} else if writeErr := os.WriteFile(filepath.Join(dir, migID+".json"), data, 0644); writeErr != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, migID+".json"), writeErr)
	}

	return record, nil
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
		data, _ := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		var rec domain.MigrationRecord
		if json.Unmarshal(data, &rec) == nil {
			result = append(result, rec)
		}
	}
	return result, nil
}
