package service

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BackupEgressWaiter makes a project's MOCO backup runs wait for their egress.
type BackupEgressWaiter interface {
	EnsureBackupEgressWait(ctx context.Context, namespace, projectID string) (bool, error)
}

// MySQLBackupWaitSweep keeps every Kubernetes MySQL project's backup CronJob
// waiting for its egress (EXC-586): one made before the wait existed, or
// remade by MOCO outside the platform's flows, gets it on the next pass.
type MySQLBackupWaitSweep struct {
	waiter    BackupEgressWaiter
	instances storage.InstanceStore
	leader    *Leadership
}

func NewMySQLBackupWaitSweep(waiter BackupEgressWaiter, instances storage.InstanceStore, leader *Leadership) *MySQLBackupWaitSweep {
	return &MySQLBackupWaitSweep{waiter: waiter, instances: instances, leader: leader}
}

// runningKubeMySQLWithBackups is a running Kubernetes MySQL project whose
// backups are on: what MOCO backs up and the MySQL sweeps tend.
func runningKubeMySQLWithBackups(inst *domain.DatabaseInstance) bool {
	return inst.DBType == domain.MySQL && inst.DeploymentMode == domain.ModeK8s && !inst.NoDatabase &&
		domain.IsActive(inst.Status) && backupsOn(inst)
}

// Run sweeps every interval until ctx ends.
func (s *MySQLBackupWaitSweep) Run(ctx context.Context, interval time.Duration) {
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
func (s *MySQLBackupWaitSweep) Sweep(ctx context.Context) {
	leading, err := s.leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: mysql backup wait sweep: leadership: %v", err)
		return
	}
	if !leading {
		return
	}
	projects, err := s.instances.FindAll()
	if err != nil {
		log.Printf("ERROR: mysql backup wait sweep: list projects: %v", err)
		return
	}
	for _, inst := range projects {
		if !runningKubeMySQLWithBackups(inst) {
			continue
		}
		if _, err := s.waiter.EnsureBackupEgressWait(ctx, inst.Namespace, inst.ProjectID); err != nil {
			log.Printf("ERROR: mysql backup wait sweep %s: %v", inst.ProjectID, err)
		}
	}
}
