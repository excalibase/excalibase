package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// BackupSchedules is the platform's backup scheduler as a project's
// lifecycle uses it. *BackupScheduler implements it.
type BackupSchedules interface {
	Register(ctx context.Context, sched *domain.BackupSchedule) error
	Unregister(ctx context.Context, projectID string) error
}

// ErrBackupSchedulerNotConfigured refuses a single-host Postgres project that
// must be backed up on a platform that runs no backup schedule.
var ErrBackupSchedulerNotConfigured = errors.New("backups are required but this platform runs no backup schedule")

// SetBackupSchedules wires the scheduler a single host's Postgres projects
// are backed up by.
func (s *ProvisioningService) SetBackupSchedules(schedules BackupSchedules) {
	s.backupSchedules = schedules
}

// platformScheduled is a project whose backups the platform itself runs: a
// single host's Postgres (MySQL has its own sweep; a cluster, its operator).
func platformScheduled(inst *domain.DatabaseInstance) bool {
	return inst.DeploymentMode == domain.ModeDocker && inst.DBType != domain.MySQL && !inst.NoDatabase && backupsOn(inst)
}

// scheduleBackups starts the project's plan schedule, and stops it again if
// the operation that registered the project rolls back (EXC-593).
func (s *ProvisioningService) scheduleBackups(ctx context.Context, inst *domain.DatabaseInstance, pc *provisioner.ProvisionContext) error {
	if !platformScheduled(inst) {
		return nil
	}
	if s.backupSchedules == nil {
		return ErrBackupSchedulerNotConfigured
	}
	retention := 0
	if inst.BackupRetentionDays != nil {
		retention = *inst.BackupRetentionDays
	}
	schedule := &domain.BackupSchedule{ProjectID: inst.ProjectID, Cron: inst.BackupSchedule, RetentionDays: retention, Enabled: true}
	if err := s.backupSchedules.Register(ctx, schedule); err != nil {
		return fmt.Errorf("schedule the project's backups: %w", err)
	}
	projectID := inst.ProjectID
	pc.RegisterCleanup("stop the project's backup schedule", func(ctx context.Context) error {
		return s.backupSchedules.Unregister(ctx, projectID)
	})
	return nil
}

// stopBackupSchedule is the deletion step that stops a project's scheduled
// backups before its database goes. Idempotent.
func (s *ProvisioningService) stopBackupSchedule(ctx context.Context, inst *domain.DatabaseInstance) error {
	return s.backupSchedules.Unregister(ctx, inst.ProjectID)
}
