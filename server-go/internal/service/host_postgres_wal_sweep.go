package service

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// HostPostgresWALSweep ships a single host's Postgres WAL as it closes, so a
// point-in-time restore reaches the last few minutes and not only the last
// backup (EXC-593). A paused project is skipped: its pause backup shipped
// what it had, and its container is down.
type HostPostgresWALSweep struct {
	adapter   *DockerBackupAdapter
	instances storage.InstanceStore
}

func NewHostPostgresWALSweep(adapter *DockerBackupAdapter, instances storage.InstanceStore) *HostPostgresWALSweep {
	return &HostPostgresWALSweep{adapter: adapter, instances: instances}
}

// Run sweeps every interval until ctx ends.
func (s *HostPostgresWALSweep) Run(ctx context.Context, interval time.Duration) {
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
func (s *HostPostgresWALSweep) Sweep(ctx context.Context) {
	projects, err := s.instances.FindAll()
	if err != nil {
		log.Printf("ERROR: WAL sweep: list projects: %v", err)
		return
	}
	for _, inst := range projects {
		if !hostPostgresArchived(inst) {
			continue
		}
		if err := s.adapter.ShipWAL(ctx, inst); err != nil {
			log.Printf("ERROR: WAL sweep %s: %v", inst.ProjectID, err)
		}
	}
}

func hostPostgresArchived(inst *domain.DatabaseInstance) bool {
	return inst.DBType != domain.MySQL && inst.DeploymentMode == domain.ModeDocker && !inst.NoDatabase &&
		inst.Status == string(domain.StatusActive) && backupsOn(inst)
}
