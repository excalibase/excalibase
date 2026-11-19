package service

import (
	"context"
	"fmt"
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
	// alerts raises a platform alert for a project whose WAL keeps failing to
	// ship (EXC-784); unset, a failure is only logged.
	alerts *failureAlerts
	now    func() time.Time
}

const shippingMetricPrefix = "wal.shipping:"

// SetAlerts raises a platform alert, mailed through notifier when it is set,
// for a project whose WAL has failed to ship on every pass for after. Closed
// WAL waits in the project's container until it ships, so a store that refuses
// uploads fills the container's disk (EXC-773 on Kubernetes).
func (s *HostPostgresWALSweep) SetAlerts(alerts *AlertingService, notifier AlertNotifier, after time.Duration, now func() time.Time) {
	s.alerts = newFailureAlerts(alerts, notifier, after, shippingMetricPrefix, describeShippingFailure)
	s.now = now
}

func describeShippingFailure(project string, failing time.Duration, cause error) string {
	return fmt.Sprintf("The WAL of project %s has failed to ship to the backup store for %s: %v. Unshipped WAL fills the project's container disk and point-in-time recovery stops at the last shipped segment",
		project, failing, cause)
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
	failures, healthy := map[string]error{}, []string{}
	for _, inst := range projects {
		if !hostPostgresArchived(inst) {
			continue
		}
		if err := s.adapter.ShipWAL(ctx, inst); err != nil {
			log.Printf("ERROR: WAL sweep %s: %v", inst.ProjectID, err)
			failures[inst.ProjectID] = err
			continue
		}
		healthy = append(healthy, inst.ProjectID)
	}
	if s.alerts != nil {
		s.alerts.track(ctx, s.now(), failures, healthy)
	}
}

func hostPostgresArchived(inst *domain.DatabaseInstance) bool {
	return inst.DBType != domain.MySQL && inst.DeploymentMode == domain.ModeDocker && !inst.NoDatabase &&
		inst.Status == string(domain.StatusActive) && backupsOn(inst)
}
