package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A MySQL project's server certificate (EXC-589) is renewed by cert-manager
// on Kubernetes; mysqld reads a renewed one only when told, and the CA the
// platform's records carry must follow the one the server presents. This
// sweep does both, under the project's lease so it never races a rotation
// writing the same records.

// mysqlTLSRoles are the logins whose records carry the server CA.
var mysqlTLSRoles = []string{roleAdmin, roleApp, roleAuthAdmin, roleWatcher}

// MaintainMySQLTLS refiles a changed server CA and reloads the certificate of
// every running MySQL project. Renewed counts projects whose records changed.
func (s *ProvisioningService) MaintainMySQLTLS(ctx context.Context) RenewalReport {
	var report RenewalReport
	instances, err := s.store.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if !maintainsMySQLTLS(inst) {
			continue
		}
		projectCtx, cancel := context.WithTimeout(ctx, renewalTimeout)
		refiled, err := s.maintainMySQLTLSOf(projectCtx, inst.ProjectID)
		cancel()
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
			continue
		}
		if refiled {
			report.Renewed++
		}
	}
	return report
}

// maintainsMySQLTLS is a MySQL project past registration whose teardown or
// restore has not started.
func maintainsMySQLTLS(inst *domain.DatabaseInstance) bool {
	return inst.DBType == domain.MySQL && !inst.NoDatabase &&
		!domain.IsNotServable(inst.Status) && !domain.IsBuildingStatus(inst.Status) &&
		inst.Status != string(domain.StageFailed)
}

func (s *ProvisioningService) maintainMySQLTLSOf(ctx context.Context, projectID string) (bool, error) {
	inst, release, err := s.holdProject(ctx, projectID, OperationTLSChange, requireServable)
	if errors.Is(err, ErrProjectOperationRunning) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer release()
	refiled, err := s.refileMySQLServerCA(ctx, inst)
	if err != nil || inst.Status != string(domain.StatusActive) {
		return refiled, err
	}
	mysql, err := s.mysqlServerOf()
	if err != nil {
		return refiled, err
	}
	return refiled, mysql.ReloadTLS(ctx, inst.Namespace, inst.ProjectID)
}

// refileMySQLServerCA puts the CA the server presents into every filed login
// that carries another one. The caller holds the project.
func (s *ProvisioningService) refileMySQLServerCA(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	mysql, err := s.mysqlServerOf()
	if err != nil {
		return false, err
	}
	serverCA, err := mysql.ServerCA(ctx, inst.Namespace, inst.ProjectID)
	if err != nil {
		return false, err
	}
	filed, err := s.filedCredentialPaths(inst.ProjectID)
	if err != nil {
		return false, err
	}
	refiled := false
	for _, role := range mysqlTLSRoles {
		path := vaultCredentialPath(inst.ProjectID, role)
		if !filed[path] {
			continue
		}
		record, err := s.vault.Get(path)
		if err != nil {
			return refiled, fmt.Errorf("read %s credential: %w", role, err)
		}
		if record[domain.CredentialServerCAKey] == serverCA {
			continue
		}
		updated := maps.Clone(record)
		updated[domain.CredentialServerCAKey] = serverCA
		if err := s.vault.Put(path, updated); err != nil {
			return refiled, fmt.Errorf("refile %s credential: %w", role, err)
		}
		refiled = true
	}
	return refiled, nil
}

// MySQLTLSMaintenance runs the sweep on a timer while this replica leads.
type MySQLTLSMaintenance struct {
	svc *ProvisioningService
}

// NewMySQLTLSMaintenance builds the timer around svc's sweep.
func NewMySQLTLSMaintenance(svc *ProvisioningService) *MySQLTLSMaintenance {
	return &MySQLTLSMaintenance{svc: svc}
}

// Start runs the sweep at once and then every interval; cancel stops it.
func (m *MySQLTLSMaintenance) Start(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		m.runIfLeader(ctx, leader)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

func (m *MySQLTLSMaintenance) runIfLeader(ctx context.Context, leader LeaderChecker) {
	ok, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: MySQL TLS maintenance: leadership check: %v", err)
		return
	}
	if !ok {
		return
	}
	report := m.svc.MaintainMySQLTLS(ctx)
	for _, err := range report.Failed {
		log.Printf("ERROR: MySQL TLS maintenance: %v (clients fail once the served certificate expires)", err)
	}
	if report.Renewed > 0 {
		log.Printf("MySQL TLS maintenance: refiled the server CA of %d project(s)", report.Renewed)
	}
}
