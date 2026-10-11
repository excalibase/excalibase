package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// admitMySQLBackups settles the backups a new MySQL database gets: the
// plan's schedule and a temporary credential for MOCO's prefix of it.
func (s *ProvisioningService) admitMySQLBackups(ctx context.Context, req *domain.ProvisioningRequest, tierType domain.TierType,
	tier config.TierConfig, projectRef string) error {
	s.applyBackupDefaults(req, tier)
	if err := s.enforceBackupTierPolicy(req, tierType, tier); err != nil {
		return err
	}
	if err := s.requireBackupTarget(req, tier); err != nil {
		return err
	}
	if req.Backup == nil || !req.Backup.Enabled {
		return nil
	}
	schedule, err := config.MySQLBackupSchedule(tierType)
	if err != nil {
		return err
	}
	req.Backup.Schedule = schedule
	if s.defaultDeploymentMode == domain.ModeDocker {
		return fmt.Errorf("backups of a single-host MySQL project are %w yet", ErrNotForMySQL)
	}
	if s.backupCreds == nil {
		return ErrBackupCredentialsNotConfigured
	}
	prefix := k8s.MOCOBackupPrefix(fmt.Sprintf("%s-%s", req.OrgID, projectRef), projectRef)
	creds, err := s.backupCreds.ForNewPrefix(ctx, req.Backup.S3, projectRef, prefix)
	if err != nil {
		return err
	}
	req.Backup.S3 = creds
	return nil
}

// moveMySQLBackupPlan puts a MySQL project's backups on the plan's schedule
// and job sizing, and records the schedule.
func (s *ProvisioningService) moveMySQLBackupPlan(ctx context.Context, inst *domain.DatabaseInstance,
	mysql *provisioner.MySQLProvisioner, orgTier domain.TierType, plan config.TierConfig) error {
	if !backupsOn(inst) {
		return nil
	}
	schedule, err := config.MySQLBackupSchedule(orgTier)
	if err != nil {
		return err
	}
	if err := mysql.SetBackupPlan(ctx, inst.Namespace, inst.ProjectID, schedule, plan); err != nil {
		return err
	}
	if inst.BackupSchedule != schedule {
		log.Printf("backups of %s move to %q with the %s plan", inst.ProjectID, schedule, orgTier)
	}
	inst.BackupSchedule = schedule
	return nil
}
