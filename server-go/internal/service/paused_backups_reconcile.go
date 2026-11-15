package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// PausedBackupsReport is what one pass over the Postgres projects did.
type PausedBackupsReport struct {
	// Suspended counts paused projects whose nightly schedule the pass turned off.
	Suspended int
	// Cleared counts the failed "hibernated" Backups it deleted.
	Cleared int
	Failed  []error
}

// ReconcilePausedBackups turns off the nightly backup of every paused Postgres
// project and deletes the Backups that failed only because the cluster was
// hibernated (EXC-758). Pausing now suspends the schedule itself; this fixes
// the projects paused before that, and any whose suspend was lost. It reads
// the cluster, not the vault, so a sealed vault does not hold it up.
func (s *ProvisioningService) ReconcilePausedBackups(ctx context.Context) PausedBackupsReport {
	var report PausedBackupsReport
	if s.k8sClient == nil || s.dockerClient != nil {
		return report
	}
	instances, err := s.store.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if inst == nil || inst.DBType != domain.PostgreSQL || inst.NoDatabase || inst.Namespace == "" {
			continue
		}
		if err := s.reconcileBackupsOf(ctx, inst, &report); err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
		}
	}
	return report
}

func (s *ProvisioningService) reconcileBackupsOf(ctx context.Context, inst *domain.DatabaseInstance, report *PausedBackupsReport) error {
	if inst.Status == string(domain.StatusPaused) {
		suspended, err := k8s.ScheduledBackupSuspended(ctx, s.k8sClient, inst.Namespace, inst.ProjectID)
		if err != nil {
			return err
		}
		if !suspended {
			if err := k8s.SetScheduledBackupSuspended(ctx, s.k8sClient, inst.Namespace, inst.ProjectID, true); err != nil {
				return err
			}
			report.Suspended++
		}
	}
	cleared, err := k8s.DeleteHibernatedBackupFailures(ctx, s.k8sClient, inst.Namespace)
	report.Cleared += cleared
	return err
}

func (s *ProvisioningService) logPausedBackups(report PausedBackupsReport) {
	for _, failure := range report.Failed {
		log.Printf("ERROR: paused project backups: %v", failure)
	}
	if report.Suspended > 0 || report.Cleared > 0 {
		log.Printf("paused project backups: %d schedule(s) suspended, %d hibernated failure(s) cleared", report.Suspended, report.Cleared)
	}
}
