package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Refusals of a database copy (EXC-667). Their words are shown to the user.
var (
	// ErrCopyNeedsBackups refuses a copy of a database with backups off:
	// copies are made from backups.
	ErrCopyNeedsBackups = errors.New("copies are made from backups; turn on backups for this database first")
	// ErrCopyNoBackupStore refuses a copy on a platform with nowhere to keep backups.
	ErrCopyNoBackupStore = errors.New("this platform has no backup store, so databases cannot be copied")
	// ErrCopyNowUnsupported refuses a copy of now where a backup cannot be taken on demand.
	ErrCopyNowUnsupported = errors.New("this database cannot be copied as it is now; pick its latest backup or a time instead")
	// ErrCopyNowNeedsRunning refuses a copy of now while the source is not running.
	ErrCopyNowNeedsRunning = errors.New("the database must be running to copy it as it is now; pick its latest backup or a time instead")
	// ErrCopyBackupFailed ends a copy of now whose backup did not complete.
	ErrCopyBackupFailed = errors.New("the backup taken for the copy did not complete, so no copy was made; try again")
)

// CheckCopy refuses, before a job is filed, a copy the source cannot give.
func (s *BackupService) CheckCopy(inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	if !backupsOn(inst) {
		return ErrCopyNeedsBackups
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return err
	}
	if !adapter.BackupsConfigured() {
		return ErrCopyNoBackupStore
	}
	if !req.CopyNow {
		return nil
	}
	if _, ok := adapter.(InPlaceCluster); !ok {
		return ErrCopyNowUnsupported
	}
	if inst.Status != string(domain.StatusActive) {
		return ErrCopyNowNeedsRunning
	}
	return nil
}

// pinCopyToNow backs the source up and points the copy at that backup. The
// source is checked again: it may have changed since the job was filed.
func (s *BackupService) pinCopyToNow(ctx context.Context, inst *domain.DatabaseInstance, adapter BackupAdapter, req domain.RestoreRequest) (domain.RestoreRequest, error) {
	if err := s.CheckCopy(inst, req); err != nil {
		return req, err
	}
	cluster, ok := adapter.(InPlaceCluster)
	if !ok {
		return req, ErrCopyNowUnsupported
	}
	ReportRestoreProgress(ctx, domain.RestoreStepSourceBackup)
	id, err := cluster.TakeSafetyBackup(ctx, inst)
	if err != nil {
		return req, fmt.Errorf("%w: %w", ErrCopyBackupFailed, err)
	}
	pinned := req
	pinned.CopyNow = false
	pinned.BackupID = id
	ReportRestoreProgress(ctx, domain.RestoreStepRestoring)
	return pinned, nil
}
