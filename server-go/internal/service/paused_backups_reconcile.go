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
	// Resumed counts running projects whose schedule was left suspended and
	// the pass turned back on.
	Resumed int
	// Cleared counts the failed "hibernated" Backups it deleted.
	Cleared int
	Failed  []error
}

// ReconcilePausedBackups turns off the nightly backup of every paused Postgres
// project, turns it back on for running ones whose cluster is not hibernated, and deletes the Backups that failed only because the cluster was
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

func (s *ProvisioningService) reconcileBackupsOf(ctx context.Context, listed *domain.DatabaseInstance, report *PausedBackupsReport) error {
	// The list may be stale: a resume can finish while the pass runs.
	inst, err := s.store.FindByProjectID(listed.ProjectID)
	if err != nil {
		return err
	}
	if inst == nil {
		return nil
	}
	switch inst.Status {
	case string(domain.StatusPaused):
		if err := s.setBackupsSuspended(ctx, inst, true, &report.Suspended); err != nil {
			return err
		}
	case string(domain.StatusActive):
		hibernated, err := k8s.ClusterHibernated(ctx, s.k8sClient, inst.Namespace, inst.ProjectID)
		if err != nil {
			return err
		}
		if !hibernated {
			if err := s.setBackupsSuspended(ctx, inst, false, &report.Resumed); err != nil {
				return err
			}
		}
	}
	cleared, err := k8s.DeleteHibernatedBackupFailures(ctx, s.k8sClient, inst.Namespace)
	report.Cleared += cleared
	return err
}

// setBackupsSuspended moves the schedule to the wanted state and counts the
// change; a schedule already there is left alone.
func (s *ProvisioningService) setBackupsSuspended(ctx context.Context, inst *domain.DatabaseInstance, want bool, changed *int) error {
	current, err := k8s.ScheduledBackupSuspended(ctx, s.k8sClient, inst.Namespace, inst.ProjectID)
	if err != nil {
		return err
	}
	if current == want {
		return nil
	}
	if err := k8s.SetScheduledBackupSuspended(ctx, s.k8sClient, inst.Namespace, inst.ProjectID, want); err != nil {
		return err
	}
	*changed++
	return nil
}

func (s *ProvisioningService) logPausedBackups(report PausedBackupsReport) {
	for _, failure := range report.Failed {
		log.Printf("ERROR: paused project backups: %v", failure)
	}
	if report.Suspended > 0 || report.Resumed > 0 || report.Cleared > 0 {
		log.Printf("paused project backups: %d schedule(s) suspended, %d resumed, %d hibernated failure(s) cleared", report.Suspended, report.Resumed, report.Cleared)
	}
}
