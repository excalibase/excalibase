package service

import (
	"errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrBackupStorageNotConfigured is returned by a restore when the platform
// has no backup object store configured. There is deliberately no fallback
// endpoint: a restore must read from wherever the backup was written.
var ErrBackupStorageNotConfigured = errors.New("backup storage not configured: set BACKUP_DEFAULT_* (or R2_*) env or vault backup/s3")

// BackupStorageSource resolves the S3-compatible store CNPG backups are
// written to. The restore path consumes the same source so endpoint,
// bucket, region and credentials can never drift between the two.
type BackupStorageSource interface {
	// BackupStorage returns the configured store, or ok=false when none is.
	BackupStorage() (*domain.S3Credentials, bool)
}

// StaticBackupStorage adapts a fixed configuration to BackupStorageSource.
// A nil config reports "not configured". Used by tests and by callers that
// already resolved the store.
func StaticBackupStorage(creds *domain.S3Credentials) BackupStorageSource {
	return staticBackupStorage{creds: creds}
}

type staticBackupStorage struct {
	creds *domain.S3Credentials
}

func (s staticBackupStorage) BackupStorage() (*domain.S3Credentials, bool) {
	if s.creds == nil {
		return nil, false
	}
	return s.creds, true
}
