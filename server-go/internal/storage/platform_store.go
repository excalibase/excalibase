package storage

import (
	"context"
	"database/sql"
	"io"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// PlatformStore is the combined interface implemented by both SQLite and Postgres stores.
// It provides all storage capabilities needed by the platform.
type PlatformStore interface {
	InstanceStore
	UserStore
	TokenStore
	OrgStore
	io.Closer

	// Audit log writes — both concrete stores (sqlite, postgres) expose
	// LogAudit/QueryAudit. The legacy AuditLogStore interface in store.go
	// drifted from the implementations and isn't actually consumed.
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
	QueryAudit(ctx context.Context, limit int) ([]domain.AuditEntry, error)

	// DB exposes the underlying *sql.DB for handlers that manage their
	// own bespoke tables (email_verifications, password_resets, etc.).
	// Both concrete impls (sqlite, postgres) already define this.
	DB() *sql.DB

	// Storage feature persistence — both concrete stores implement these
	// alongside the existing methods (see sqlite_storage.go / pg_storage.go).
	storagesvc.BucketStore

	// BackupRecords returns the BackupRecordStore for the underlying
	// engine. Implemented by sqlite.NewBackupRecords / pg.NewBackupRecords.
	BackupRecords() BackupRecordStore

	// BackupSchedules returns the BackupScheduleStore the platform-side
	// scheduler reloads schedules from on Start.
	BackupSchedules() BackupScheduleStore

	// RestoreJobs returns the RestoreJobStore the orchestrator writes
	// async restore state into.
	RestoreJobs() RestoreJobStore
}
