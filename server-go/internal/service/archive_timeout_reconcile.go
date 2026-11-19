package service

import (
	"context"
	"fmt"
	"log"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// ArchiveTimeoutReport is what one pass over the Postgres projects did.
type ArchiveTimeoutReport struct {
	// Updated counts clusters the pass moved to their plan's upload interval.
	Updated int
	Failed  []error
}

// ReconcileArchiveTimeouts gives every existing Postgres cluster the WAL upload
// interval its plan names (EXC-784). A new cluster and a plan change already
// render it; this reaches the clusters made before. The setting reloads without
// a restart. A paused project is included: its cluster is hibernated, and the
// resume then ships hourly. Plans that name no interval are left alone, and a
// project that moved off Free, or any project while the guard is off, has the
// setting removed so the operator's interval returns. On a single host the same
// pass reads each running container's setting and reloads it (back to five
// minutes when the plan names none); a restored or upgraded project, which comes
// back on the default, is reached the same way.
func (s *ProvisioningService) ReconcileArchiveTimeouts(ctx context.Context) ArchiveTimeoutReport {
	var report ArchiveTimeoutReport
	if s.dockerClient != nil {
		return s.reconcileHostArchiveTimeouts(ctx)
	}
	if s.k8sClient == nil {
		return report
	}
	instances, err := s.store.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if !hasCluster(inst) {
			continue
		}
		updated, err := s.reconcileArchiveTimeoutOf(ctx, inst)
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
		}
		if updated {
			report.Updated++
		}
	}
	return report
}

// hasCluster is a Kubernetes Postgres project whose cluster exists to be read:
// not one still being built, failed, or being torn down.
func hasCluster(inst *domain.DatabaseInstance) bool {
	return inst != nil && inst.DBType == domain.PostgreSQL && !inst.NoDatabase && inst.Namespace != "" &&
		!domain.IsDeletionStatus(inst.Status) && !domain.IsBuildingStatus(inst.Status) && inst.Status != string(domain.StageFailed)
}

func (s *ProvisioningService) reconcileArchiveTimeoutOf(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	plan, err := s.tierConfig(ctx, inst.Tier)
	if err != nil {
		return false, err
	}
	name := inst.ProjectID + postgresClusterSuffix
	for attempt := 0; attempt < clusterUpdateAttempts; attempt++ {
		cluster, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, name)
		if err != nil {
			return false, fmt.Errorf("read cluster %s: %w", name, err)
		}
		changed, differs := k8s.WithArchiveTimeout(cluster, plan.ArchiveTimeout)
		if !differs {
			return false, nil
		}
		err = s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, changed)
		if err == nil {
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, fmt.Errorf("update cluster %s: %w", name, err)
		}
	}
	return false, fmt.Errorf("update cluster %s: still conflicting after %d attempts", name, clusterUpdateAttempts)
}

func logArchiveTimeouts(report ArchiveTimeoutReport) {
	for _, failure := range report.Failed {
		log.Printf("ERROR: archive timeout reconcile: %v", failure)
	}
	if report.Updated > 0 {
		log.Printf("archive timeout reconcile: %d cluster(s) moved to their plan's WAL upload interval", report.Updated)
	}
}

// reconcileHostArchiveTimeouts is the pass on a single host: each running
// Postgres project with backups has its container moved to its plan's
// interval. A paused project's container is down and is reached after resume.
func (s *ProvisioningService) reconcileHostArchiveTimeouts(ctx context.Context) ArchiveTimeoutReport {
	var report ArchiveTimeoutReport
	instances, err := s.store.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	postgres := provisioner.NewDockerPostgreSQLProvisioner(s.dockerClient)
	for _, inst := range instances {
		if inst == nil || !hostPostgresArchived(inst) {
			continue
		}
		plan, err := s.tierConfig(ctx, inst.Tier)
		if err == nil {
			var changed bool
			if changed, err = postgres.SetArchiveTimeout(ctx, inst.Namespace, provisioner.ArchiveTimeoutOf(plan)); changed {
				report.Updated++
			}
		}
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
		}
	}
	return report
}
