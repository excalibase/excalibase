package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// RoleCertificateRenewerConfig wires the renewer.
type RoleCertificateRenewerConfig struct {
	Instances storage.InstanceStore
	Kube      k8s.KubeClient
	Vault     vaultclient.VaultClient
	// RestartReplication redeploys a running project's watcher onto its
	// renewed certificate; the watcher reads the files only at start.
	RestartReplication func(ctx context.Context, inst *domain.DatabaseInstance) error
	Now                func() time.Time
}

// RoleCertificateRenewer re-issues the platform roles' client certificates
// before they expire, and republishes the cluster CA when the operator renews
// it (EXC-410). Consumers re-read the vault record: the control plane's pools
// are reopened within an hour, the engine and auth refresh on their own
// schedule, and the watcher is redeployed here.
type RoleCertificateRenewer struct {
	cfg RoleCertificateRenewerConfig
}

// NewRoleCertificateRenewer builds a renewer.
func NewRoleCertificateRenewer(cfg RoleCertificateRenewerConfig) *RoleCertificateRenewer {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &RoleCertificateRenewer{cfg: cfg}
}

// RenewDue renews every Kubernetes project whose platform role certificates
// are inside the renewal window or no longer carry the current cluster CA.
func (r *RoleCertificateRenewer) RenewDue(ctx context.Context) RenewalReport {
	var report RenewalReport
	instances, err := r.cfg.Instances.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if !holdsRoleCertificates(inst) {
			continue
		}
		projectCtx, cancel := context.WithTimeout(ctx, renewalTimeout)
		renewed, err := r.renewProject(projectCtx, inst)
		cancel()
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
			continue
		}
		if renewed {
			report.Renewed++
		}
	}
	return report
}

// holdsRoleCertificates is a Kubernetes Postgres project past registration
// whose teardown has not started.
func holdsRoleCertificates(inst *domain.DatabaseInstance) bool {
	return inst.DeploymentMode != domain.ModeDocker && inst.DBType == domain.PostgreSQL && !inst.NoDatabase &&
		!domain.IsDeletionStatus(inst.Status) && !domain.IsBuildingStatus(inst.Status) &&
		inst.Status != string(domain.StatusRestoring) && inst.Status != string(domain.StageFailed)
}

func (r *RoleCertificateRenewer) renewProject(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	ca, err := readClusterCA(ctx, r.cfg.Kube, inst.Namespace, inst.ProjectID)
	if err != nil {
		return false, err
	}
	renewedWatcher := false
	renewedAny := false
	for _, role := range tenantcert.PlatformRoles {
		renewed, err := r.renewRole(inst.ProjectID, role, ca)
		if err != nil {
			return renewedAny, err
		}
		renewedAny = renewedAny || renewed
		renewedWatcher = renewedWatcher || (renewed && role == roleWatcher)
	}
	if renewedWatcher && inst.Status == string(domain.StatusActive) {
		if err := r.cfg.RestartReplication(ctx, inst); err != nil {
			return true, fmt.Errorf("redeploy the watcher onto its renewed certificate: %w", err)
		}
	}
	return renewedAny, nil
}

func (r *RoleCertificateRenewer) renewRole(projectID, role string, ca tenantcert.CA) (bool, error) {
	path := vaultCredentialPath(projectID, role)
	record, err := r.cfg.Vault.Get(path)
	if err != nil {
		return false, fmt.Errorf("read %s credential: %w", role, err)
	}
	if !tenantcert.RenewalDue(record, string(ca.CertPEM), r.cfg.Now()) {
		return false, nil
	}
	material, err := tenantcert.Issue(ca, role, r.cfg.Now())
	if err != nil {
		return false, err
	}
	if err := r.cfg.Vault.Put(path, material.AddTo(record)); err != nil {
		return false, fmt.Errorf("file renewed %s certificate: %w", role, err)
	}
	return true, nil
}

// Start renews on a timer while this replica leads; the first pass runs at once.
func (r *RoleCertificateRenewer) Start(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		r.renewIfLeader(ctx, leader)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.renewIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

func (r *RoleCertificateRenewer) renewIfLeader(ctx context.Context, leader LeaderChecker) {
	ok, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: role certificate renewal: leadership check: %v", err)
		return
	}
	if !ok {
		return
	}
	report := r.RenewDue(ctx)
	for _, err := range report.Failed {
		log.Printf("ERROR: role certificate renewal: %v (platform logins fail once the current certificate expires)", err)
	}
	if report.Renewed > 0 {
		log.Printf("role certificate renewal: renewed %d project(s)", report.Renewed)
	}
}

