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
	SetupTokenStore
	TokenStore
	OrgStore
	TierConfigStore
	ProjectActivityStore
	EmailVerificationStore
	StudioIdentityStore
	io.Closer

	// Audit log writes — both concrete stores (sqlite, postgres) expose
	// LogAudit/QueryAudit. The legacy AuditLogStore interface in store.go
	// drifted from the implementations and isn't actually consumed.
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
	QueryAudit(ctx context.Context, limit int) ([]domain.AuditEntry, error)
	QueryProjectAudit(ctx context.Context, projectID, via string, limit int) ([]domain.AuditEntry, error)

	// DB exposes the underlying *sql.DB for handlers that manage their
	// own bespoke tables (email_verifications, password_resets, etc.).
	// The concrete Postgres store defines this.
	DB() *sql.DB

	// Storage feature persistence — the concrete store implements these
	// alongside the existing methods (see pg_storage.go).
	storagesvc.BucketStore

	// BackupRecords returns the BackupRecordStore for the underlying
	// engine. Implemented by pg.NewBackupRecords.
	BackupRecords() BackupRecordStore

	// BackupSchedules returns the BackupScheduleStore the platform-side
	// scheduler reloads schedules from on Start.
	BackupSchedules() BackupScheduleStore

	// RestoreJobs returns the RestoreJobStore the orchestrator writes
	// async restore state into.
	RestoreJobs() RestoreJobStore

	// Permissions returns the PermissionStore behind
	// /api/provision/{p}/permissions, tracked-functions and
	// function-permissions (EXC-370 step C).
	Permissions() PermissionStore
}
