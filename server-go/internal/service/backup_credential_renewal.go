package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// renewalTimeout bounds one project's renewal, so a hung store cannot stall
// every other project's.
const renewalTimeout = 30 * time.Second

// BackupCredentialRenewerConfig wires the renewer.
type BackupCredentialRenewerConfig struct {
	Instances storage.InstanceStore
	Kube      k8s.KubeClient
	// Storage is the platform store credentials are derived from.
	Storage BackupStorageSource
	Issuer  *BackupCredentialIssuer
	Now     func() time.Time
}

// BackupCredentialRenewer keeps every archiving project's temporary
// credential alive. The Barman Cloud sidecar reads the Secret on each WAL
// segment (its cache lives 10 s), so replacing the Secret's data is enough;
// nothing restarts. A renewal that fails is logged and counted, and the
// credential it could not replace keeps working until its expiry, so an
// outage shorter than half the lifetime costs nothing.
type BackupCredentialRenewer struct {
	cfg BackupCredentialRenewerConfig
}

// RenewalReport is what one pass did.
type RenewalReport struct {
	Renewed int
	Failed  []error
}

// NewBackupCredentialRenewer builds a renewer.
func NewBackupCredentialRenewer(cfg BackupCredentialRenewerConfig) *BackupCredentialRenewer {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &BackupCredentialRenewer{cfg: cfg}
}

// RenewDue replaces every archiving project's credential that has less than
// half its lifetime left.
func (r *BackupCredentialRenewer) RenewDue(ctx context.Context) RenewalReport {
	var report RenewalReport
	metrics.ResetBackupCredentialsExpiry()
	instances, err := r.cfg.Instances.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if !archivesToObjectStore(inst) {
			continue
		}
		projectCtx, cancel := context.WithTimeout(ctx, renewalTimeout)
		renewed, err := r.renewProject(projectCtx, inst)
		cancel()
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
			metrics.CountBackupCredentialRenewal(false)
			continue
		}
		if renewed {
			report.Renewed++
			metrics.CountBackupCredentialRenewal(true)
		}
	}
	return report
}

// archivesToObjectStore is a Kubernetes project with backups whose teardown
// has not started.
func archivesToObjectStore(inst *domain.DatabaseInstance) bool {
	return inst.DeploymentMode != domain.ModeDocker && !inst.NoDatabase &&
		inst.BackupEnabled != nil && *inst.BackupEnabled &&
		!domain.IsDeletionStatus(inst.Status) && inst.Status != string(domain.StageFailed)
}

func (r *BackupCredentialRenewer) renewProject(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	current, err := r.cfg.Kube.GetSecret(ctx, inst.Namespace, k8s.BackupCredentialsSecretName)
	if err != nil {
		if domain.IsBuildingStatus(inst.Status) || inst.Status == string(domain.StatusRestoring) {
			return false, nil // its pipeline has not written the secret yet
		}
		return false, fmt.Errorf("read backup credentials: %w", err)
	}
	store, ok := r.cfg.Storage.BackupStorage()
	if !ok {
		return false, ErrBackupStorageNotConfigured
	}
	if err := sameStore(current, store); err != nil {
		return false, err
	}
	if !r.due(inst.ProjectID, current, store) {
		return false, nil
	}
	creds, err := r.cfg.Issuer.Renew(ctx, store, inst.ProjectID)
	if err != nil {
		return false, err
	}
	data, err := k8s.BackupCredentialsSecretData(creds)
	if err != nil {
		return false, err
	}
	if err := r.cfg.Kube.UpdateSecret(ctx, inst.Namespace, k8s.BackupCredentialsSecretName, data); err != nil {
		return false, fmt.Errorf("write backup credentials: %w", err)
	}
	metrics.SetBackupCredentialsExpiry(inst.ProjectID, creds.ExpiresAt)
	return true, nil
}

// due is a credential past half its life, of unknown expiry, or derived from
// a platform key that is no longer the current one.
func (r *BackupCredentialRenewer) due(projectID string, current map[string][]byte, store *domain.S3Credentials) bool {
	expiry, err := k8s.BackupCredentialsExpiry(current)
	if err != nil {
		return true
	}
	metrics.SetBackupCredentialsExpiry(projectID, expiry)
	if string(current[k8s.BackupCredentialsIssuedByKey]) != k8s.BackupKeyFingerprint(store) {
		return true
	}
	return expiry.Sub(r.cfg.Now()) <= r.cfg.Issuer.RenewWithin()
}

// sameStore refuses to renew a project against a store its cluster does not
// archive to: its ObjectStore still names the old bucket and endpoint.
func sameStore(current map[string][]byte, store *domain.S3Credentials) error {
	bucket, endpoint := string(current[k8s.BackupCredentialsBucketKey]), string(current[k8s.BackupCredentialsEndpointKey])
	if (bucket != "" && bucket != store.Bucket) || (endpoint != "" && endpoint != store.Endpoint) {
		return fmt.Errorf("the project archives to bucket %q at %q but the platform store is now bucket %q at %q", bucket, endpoint, store.Bucket, store.Endpoint)
	}
	return nil
}

// Start renews on a timer while this replica leads. The first pass runs at
// once, so a replica that was down past half-life catches up immediately.
func (r *BackupCredentialRenewer) Start(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
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

func (r *BackupCredentialRenewer) renewIfLeader(ctx context.Context, leader LeaderChecker) {
	if ok, err := leader.IsLeader(ctx); err != nil || !ok {
		metrics.ResetBackupCredentialsExpiry()
		if err != nil {
			log.Printf("ERROR: backup credential renewal: leadership check: %v", err)
		}
		return
	}
	report := r.RenewDue(ctx)
	for _, err := range report.Failed {
		log.Printf("ERROR: backup credential renewal: %v (WAL archiving stops when the current credential expires)", err)
	}
	if report.Renewed > 0 {
		log.Printf("backup credential renewal: renewed %d project credential(s)", report.Renewed)
	}
}
