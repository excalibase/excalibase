package service

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// HostMySQLBackupSweep is a single host's MySQL backup schedule: every pass
// backs up the running projects whose last run is an interval old. A paused
// project is skipped; it has written nothing since its last run. Retention
// runs in the deletion sweep, as for every engine.
type HostMySQLBackupSweep struct {
	adapter   *HostMySQLBackupAdapter
	instances storage.InstanceStore
}

func NewHostMySQLBackupSweep(adapter *HostMySQLBackupAdapter, instances storage.InstanceStore) *HostMySQLBackupSweep {
	return &HostMySQLBackupSweep{adapter: adapter, instances: instances}
}

// Run sweeps every interval until ctx ends.
func (s *HostMySQLBackupSweep) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep is one pass; a project's failure is logged and the next goes on.
func (s *HostMySQLBackupSweep) Sweep(ctx context.Context) {
	projects, err := s.instances.FindAll()
	if err != nil {
		log.Printf("ERROR: mysql backup sweep: list projects: %v", err)
		return
	}
	for _, inst := range projects {
		if !hostMySQLBackedUp(inst) {
			continue
		}
		s.sweepProject(ctx, inst)
	}
}

func hostMySQLBackedUp(inst *domain.DatabaseInstance) bool {
	return inst.DBType == domain.MySQL && inst.DeploymentMode == domain.ModeDocker && domain.IsActive(inst.Status) &&
		inst.BackupEnabled != nil && *inst.BackupEnabled
}

func (s *HostMySQLBackupSweep) sweepProject(ctx context.Context, inst *domain.DatabaseInstance) {
	due, err := s.adapter.BackupDue(ctx, inst)
	if err != nil {
		log.Printf("ERROR: mysql backup sweep %s: %v", inst.ProjectID, err)
		return
	}
	if !due {
		return
	}
	if _, err := s.adapter.RunScheduled(ctx, inst); err != nil {
		log.Printf("ERROR: mysql backup sweep %s: %v", inst.ProjectID, err)
	}
}
