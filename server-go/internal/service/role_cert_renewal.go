package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
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
	// HoldProject takes the project's lifecycle lease and reads it under the
	// lease, admitting only an ACTIVE project; ErrProjectOperationRunning when
	// another operation holds it.
	HoldProject func(ctx context.Context, projectID string) (*domain.DatabaseInstance, func(), error)
	Now         func() time.Time
}

// RoleCertificateRenewer re-issues the platform roles' client certificates
// before they expire, and republishes the cluster CA when the operator renews
// it (EXC-410). Consumers re-read the vault record: the control plane's pools
// are reopened within an hour, the engine and auth refresh on their own
// schedule, and the watcher is redeployed here.
type RoleCertificateRenewer struct {
	cfg RoleCertificateRenewerConfig
	// alerts raises a platform alert for a project whose renewal or watcher
	// restart keeps failing (EXC-887); unset, failures are only logged.
	alerts *failureAlerts
}

// errNoProjectLease refuses a watcher redeploy the renewer was wired without a lease for.
var errNoProjectLease = errors.New("the renewer has no project lease to redeploy a watcher under")

const roleCertMetricPrefix = "role.cert.renewal:"

// SetAlerts raises a platform alert, mailed through notifier when it is set,
// for a project whose renewal has failed on every pass for after.
func (r *RoleCertificateRenewer) SetAlerts(alerts *AlertingService, notifier AlertNotifier, after time.Duration) {
	r.alerts = newFailureAlerts(alerts, notifier, after, roleCertMetricPrefix, describeRoleCertFailure)
}

func describeRoleCertFailure(project string, failing time.Duration, cause error) string {
	return fmt.Sprintf("The platform role certificates or the CDC watcher of project %s could not be brought up to date for %s: %v. Realtime and subscriptions stop when the current certificate or CA expires",
		project, failing, cause)
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
	report := RenewalReport{failures: map[string]error{}}
	defer func() {
		if r.alerts != nil && report.listed {
			r.alerts.track(ctx, r.cfg.Now(), report.failures, report.healthy)
		}
	}()
	instances, err := r.cfg.Instances.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	report.listed = true
	for _, inst := range instances {
		if !holdsRoleCertificates(inst) {
			continue
		}
		renewed, err := r.renewProject(ctx, inst)
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
			report.failures[inst.ProjectID] = err
			continue
		}
		report.healthy = append(report.healthy, inst.ProjectID)
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

// renewProject takes the pass's context: the reads and the renewal run within
// renewalTimeout, the watcher restart within its own budget.
func (r *RoleCertificateRenewer) renewProject(pass context.Context, inst *domain.DatabaseInstance) (bool, error) {
	ctx, cancel := context.WithTimeout(pass, renewalTimeout)
	defer cancel()
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
	if inst.Status == string(domain.StatusActive) {
		if err := r.restartStaleWatcher(pass, inst, renewedWatcher); err != nil {
			return renewedAny, err
		}
	}
	return renewedAny, nil
}

// restartStaleWatcher redeploys the watcher when this pass renewed its
// certificate, or when a running watcher still carries another one than vault
// holds: an earlier pass renewed it and its restart failed, and nothing else
// would ever redeploy it (EXC-887). The pod, not a marker, is the record, so
// the retry survives a restart or a change of leader and repeats every pass
// until it works.
func (r *RoleCertificateRenewer) restartStaleWatcher(pass context.Context, inst *domain.DatabaseInstance, renewed bool) error {
	if !renewed {
		readCtx, cancel := context.WithTimeout(pass, renewalTimeout)
		stale, err := r.watcherIsStale(readCtx, inst)
		cancel()
		if err != nil || !stale {
			return err
		}
		log.Printf("role certificate renewal: project %s: its watcher runs an older certificate than vault holds, or none runs; redeploying it", inst.ProjectID)
	}
	return r.restartUnderLease(pass, inst.ProjectID)
}

// restartUnderLease redeploys the watcher while holding the project's lease,
// on the project as the lease reads it: the pass's snapshot can be minutes old
// and a pause since then must win. A project another operation holds, or that
// is no longer active, is left; the next pass finds the watcher still stale.
func (r *RoleCertificateRenewer) restartUnderLease(pass context.Context, projectID string) error {
	if r.cfg.HoldProject == nil {
		return errNoProjectLease
	}
	held, release, err := r.cfg.HoldProject(pass, projectID)
	if errors.Is(err, ErrProjectOperationRunning) || errors.Is(err, ErrProjectNotActive) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hold the project to redeploy its watcher: %w", err)
	}
	defer release()
	// Helm waits for the pods to be ready: a shorter deadline cancels an
	// upgrade half way and leaves the release pending.
	ctx, cancel := context.WithTimeout(pass, k8s.HelmApplyBudget)
	defer cancel()
	if err := r.cfg.RestartReplication(ctx, held); err != nil {
		return fmt.Errorf("redeploy the watcher onto its renewed certificate: %w", err)
	}
	return nil
}

// watcherIsStale is a live watcher pod whose certificate fingerprint differs
// from the one filed in vault, or no live pod at all for a project whose
// watcher was ever deployed (its certificate Secret exists): a redeploy that
// removed the release and then failed leaves exactly that.
func (r *RoleCertificateRenewer) watcherIsStale(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	record, err := r.cfg.Vault.Get(vaultCredentialPath(inst.ProjectID, roleWatcher))
	if err != nil {
		return false, fmt.Errorf("read %s credential: %w", roleWatcher, err)
	}
	material, err := tenantcert.FromRecord(record)
	if err != nil {
		return false, fmt.Errorf("%s: %w", roleWatcher, err)
	}
	pods, err := r.cfg.Kube.GetPods(ctx, inst.Namespace, provisioner.WatcherComponentLabel+"="+provisioner.WatcherComponentValue)
	if err != nil {
		return false, fmt.Errorf("list the watcher pods: %w", err)
	}
	want := provisioner.WatcherCertFingerprint(material)
	live := 0
	for _, pod := range pods {
		if !runsTheWatcher(pod) {
			continue
		}
		live++
		if pod.Annotations[provisioner.WatcherCertAnnotation] != want {
			return true, nil
		}
	}
	if live > 0 {
		return false, nil
	}
	return r.watcherWasDeployed(ctx, inst)
}

func (r *RoleCertificateRenewer) watcherWasDeployed(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	_, err := r.cfg.Kube.GetSecret(ctx, inst.Namespace, provisioner.WatcherTLSSecretName(inst.ProjectID))
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the watcher certificate secret: %w", err)
	}
	return true, nil
}

func (r *RoleCertificateRenewer) renewRole(projectID, role string, ca tenantcert.CA) (bool, error) {
	path := vaultCredentialPath(projectID, role)
	record, err := r.cfg.Vault.Get(path)
	if err != nil {
		if role == roleScheduler && !r.filed(projectID, path) {
			// Not filed yet: the platform role reconcile files it with its certificate.
			return false, nil
		}
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

// filed reports whether vault lists path among the project's credentials.
func (r *RoleCertificateRenewer) filed(projectID, path string) bool {
	paths, err := r.cfg.Vault.List(vaultCredentialPrefix(projectID))
	return err == nil && slices.Contains(paths, path)
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

// runsTheWatcher is a pod that is, or is about to be, serving: an evicted or
// terminating one keeps its old fingerprint for good and says nothing about
// what the watcher runs.
func runsTheWatcher(pod corev1.Pod) bool {
	return pod.DeletionTimestamp == nil && (pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending)
}
