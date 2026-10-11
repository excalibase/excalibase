package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// RetentionPurger deletes a project's backups that retention no longer
// keeps; adapters whose engine never deletes its own backups implement it.
type RetentionPurger interface {
	PurgeExpired(ctx context.Context, inst *domain.DatabaseInstance) (int, error)
}

// BackupRetentionWork is the retention pass the deletion sweeper drives.
type BackupRetentionWork interface {
	PurgeExpiredBackups(ctx context.Context) BackupRetentionReport
}

// BackupRetentionReport is what one retention pass did.
type BackupRetentionReport struct {
	Purged  []string
	Failed  []string
	Objects int
}

// PurgeExpiredBackups runs retention for every live project whose adapter
// keeps it; the rest (CNPG retention, teardowns) are left to their owners.
func (s *BackupService) PurgeExpiredBackups(ctx context.Context) BackupRetentionReport {
	var report BackupRetentionReport
	instances, err := s.store.FindAll()
	if err != nil {
		log.Printf("backup retention: list projects: %v", err)
		return report
	}
	for _, inst := range instances {
		if !backupsOn(inst) || inst.NoDatabase || domain.IsDeletionStatus(inst.Status) || inst.Status == string(domain.StageFailed) {
			continue
		}
		adapter, err := s.adapterFor(inst)
		if err != nil {
			continue
		}
		purger, ok := adapter.(RetentionPurger)
		if !ok {
			continue
		}
		deleted, err := purger.PurgeExpired(ctx, inst)
		report.Objects += deleted
		if err != nil {
			log.Printf("backup retention: %s: %v", inst.ProjectID, err)
			report.Failed = append(report.Failed, inst.ProjectID)
			continue
		}
		report.Purged = append(report.Purged, inst.ProjectID)
	}
	return report
}

func (r BackupRetentionReport) String() string {
	return fmt.Sprintf("projects=%d failed=%d objects=%d", len(r.Purged), len(r.Failed), r.Objects)
}
