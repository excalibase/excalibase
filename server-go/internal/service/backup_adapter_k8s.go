package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// K8sBackupAdapter takes, lists and restores CNPG backups, all through the
// Barman Cloud plugin.
type K8sBackupAdapter struct {
	k8sClient k8s.KubeClient
	// storage is the object store backups are written to; restores read
	// from it. Resolved per call so vault rotation is picked up.
	storage BackupStorageSource
	// instances is consulted before a restore creates anything, so a target
	// id that is already registered is refused rather than built over.
	instances storage.InstanceStore
	// registrar finishes a restore the way a provision ends: roles, vault,
	// instance row, events. Restore refuses to run without it —
	// a restored project nobody registered is invisible to the API.
	registrar ProjectRegistrar
	// probe proves the recovered database answers a query before the
	// restored project is allowed to become ACTIVE.
	probe DatabaseProbe
	// poller bounds the wait for the recovered cluster. A restore replays
	// WAL, so the default budget is generous; the clock is injected so
	// tests observe the wait without sleeping.
	poller provisioner.Poller
	// publicDomainSuffix names the restored project's public endpoint, so
	// its server certificate can carry it. Empty without public endpoints.
	publicDomainSuffix string
	// plans decides the restored project's tier and backups, exactly as they
	// would be decided for a new project.
	plans RestorePlanSource
	// creds mints the restore's credentials: read-only for the source's
	// prefix, read-write for the restored project's own.
	creds *BackupCredentialIssuer
	// targetGuard proves a point-in-time target recoverable before anything
	// is created; without one such a restore is refused.
	targetGuard RestoreTargetGuard
}

// SetBackupCredentials wires the issuer; without one a restore is refused.
func (a *K8sBackupAdapter) SetBackupCredentials(i *BackupCredentialIssuer) { a.creds = i }

// RestorePlan is what a restored project is created with.
type RestorePlan struct {
	Tier   domain.TierType
	Config config.TierConfig
	// Backup is nil when a new project of this tier would get no backups.
	Backup *domain.BackupSettings
}

// RestorePlanSource decides the plan for a project restored from source.
type RestorePlanSource interface {
	RestorePlan(ctx context.Context, source *domain.DatabaseInstance) (RestorePlan, error)
}

// ErrRestorePlanNotConfigured is returned when nothing can say what a restored
// project is created with. There is no default size.
var ErrRestorePlanNotConfigured = errors.New("restore: the restored project's tier cannot be resolved")

// ErrRestoreBackupUnscheduled refuses a restore whose plan enables backups
// without saying when they run.
var ErrRestoreBackupUnscheduled = errors.New("restore: the backup plan has no schedule")

// ErrProjectRegistrarNotConfigured is returned when a restore would produce a
// database the platform cannot register as a project.
var ErrProjectRegistrarNotConfigured = errors.New("restore: project registrar not configured")

const (
	defaultRestoreReadyTimeout = 15 * time.Minute
	defaultRestoreReadyPoll    = 5 * time.Second
	// postgresClusterSuffix matches the CNPG Cluster name the CRD builder
	// stamps onto a project.
	postgresClusterSuffix = "-postgres"
)

// NewK8sBackupAdapter wires the CNPG adapter. storage must be the same
// source the provisioner writes backups with (ProvisioningService
// implements it); nil means restores fail with ErrBackupStorageNotConfigured.
func NewK8sBackupAdapter(client k8s.KubeClient, storage BackupStorageSource) *K8sBackupAdapter {
	return &K8sBackupAdapter{
		k8sClient:   client,
		storage:     storage,
		poller:      provisioner.NewPoller(defaultRestoreReadyPoll, defaultRestoreReadyTimeout),
		targetGuard: NewArchivedWALGuard(client),
	}
}

// SetRestoreTargetGuard replaces the point-in-time target check.
func (a *K8sBackupAdapter) SetRestoreTargetGuard(g RestoreTargetGuard) { a.targetGuard = g }

// SetPublicDomainSuffix names the suffix public endpoint names hang off.
func (a *K8sBackupAdapter) SetPublicDomainSuffix(suffix string) { a.publicDomainSuffix = suffix }

// SetDatabaseProbe wires the check that proves a recovered database serves
// queries. Without it a restore refuses to run.
func (a *K8sBackupAdapter) SetDatabaseProbe(p DatabaseProbe) { a.probe = p }

// SetReadyPoller replaces the bounded wait for the recovered cluster.
func (a *K8sBackupAdapter) SetReadyPoller(p provisioner.Poller) { a.poller = p }

// SetRestoreReadyTimeout bounds the wait for the recovered cluster on the
// wall clock.
func (a *K8sBackupAdapter) SetRestoreReadyTimeout(d time.Duration) {
	a.poller = provisioner.NewPoller(defaultRestoreReadyPoll, d)
}

// SetProjectRegistrar wires the shared registration path. Called from main.go
// once the provisioning service exists.
func (a *K8sBackupAdapter) SetProjectRegistrar(r ProjectRegistrar) { a.registrar = r }

// SetRestorePlanSource wires what decides a restored project's tier and
// backups. Without it a restore refuses to run.
func (a *K8sBackupAdapter) SetRestorePlanSource(p RestorePlanSource) { a.plans = p }

// SetInstanceStore wires the store a restore checks its target id against.
func (a *K8sBackupAdapter) SetInstanceStore(s storage.InstanceStore) { a.instances = s }

// Configure is a no-op for the K8s adapter today. CNPG ScheduledBackup
// CRDs are written by the provisioner during the BACKUP_CONFIGURATION
// stage; in-life schedule changes will land in a future iteration when
// the adapter owns the ScheduledBackup CRD lifecycle end-to-end.
func (a *K8sBackupAdapter) Configure(_ context.Context, _ *domain.DatabaseInstance, _ string, _ int) error {
	return nil
}

func (a *K8sBackupAdapter) TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	backupName := fmt.Sprintf("%s-backup-%s", inst.ProjectID, time.Now().Format("20060102-150405"))
	backup := k8s.BuildManualBackup(inst.ProjectID, inst.Namespace, backupName)
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGBackupGVR, inst.Namespace, backup); err != nil {
		return BackupRef{}, fmt.Errorf("trigger backup: %w", err)
	}
	return BackupRef{
		ID:        backupName,
		ProjectID: inst.ProjectID,
		Type:      backupTypeManual,
		Status:    backupStatusInProgress,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// BackupsConfigured reports whether an object store is wired. Without one
// CNPG has nowhere to put a backup.
func (a *K8sBackupAdapter) BackupsConfigured() bool {
	_, ok := a.backupStorage()
	return ok
}

// List reports the project's backups as the operator does: every Backup of
// its cluster, manual or scheduled, with the phase the operator gave it.
func (a *K8sBackupAdapter) List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error) {
	objects, err := a.k8sClient.ListCRDs(ctx, k8s.CNPGBackupGVR, inst.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list backups of %s: %w", inst.ProjectID, err)
	}
	cluster := inst.ProjectID + postgresClusterSuffix
	refs := make([]BackupRef, 0, len(objects))
	for _, obj := range objects {
		if name, _, _ := unstructured.NestedString(obj.Object, "spec", "cluster", "name"); name == cluster {
			refs = append(refs, backupRefOf(inst.ProjectID, obj))
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].StartedAt < refs[j].StartedAt })
	return refs, nil
}

// Restore bootstraps a new CNPG cluster from the source project's backups and
// registers it as a project. The object store (endpoint, bucket, credentials)
// comes from the same BackupStorageSource the backup-write path uses; there is
// no fallback.
//
// Failure semantics once the Cluster exists: everything the restore created
// is compensated away in LIFO order — the Cluster CR, the namespace (which
// takes the object-store secret with it) and every vault credential, NATS
// identity and watcher registration the project registration filed. The
// caller is told only that the restore was not confirmed; the step that gave
// up and what it saw go to the log.
func (a *K8sBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	store, ok := a.backupStorage()
	if !ok {
		return nil, ErrBackupStorageNotConfigured
	}
	if a.registrar == nil {
		return nil, ErrProjectRegistrarNotConfigured
	}
	if a.probe == nil {
		return nil, ErrDatabaseProbeNotConfigured
	}
	if a.plans == nil {
		return nil, ErrRestorePlanNotConfigured
	}
	if inst.OrgID == "" {
		return nil, fmt.Errorf("restore source %s: %w", inst.ProjectID, k8s.ErrProjectOrgRequired)
	}
	image, err := config.PostgresImage(inst.PostgresVersion)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	if inst.DocumentDB && !config.DocumentDBSupported(inst.PostgresVersion) {
		return nil, fmt.Errorf("restore %s: %w: postgres %s", inst.ProjectID, ErrDocumentDBNotAvailableOnMajor, inst.PostgresVersion)
	}
	newProject := req.TargetProjectID
	altNames, err := provisioner.PublicServerNames(newProject, a.publicDomainSuffix)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	if err := assertProjectIDAvailable(a.instances, newProject); err != nil {
		return nil, err
	}
	plan, err := a.plans.RestorePlan(ctx, inst)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	recoveryTarget, err := a.recoveryTarget(ctx, inst, req)
	if err != nil {
		return nil, err
	}
	if err := a.ensureTargetRecoverable(ctx, inst, req); err != nil {
		return nil, err
	}
	target := restoreTarget{store: store, plan: plan, project: newProject, namespace: fmt.Sprintf("%s-%s", inst.OrgID, newProject)}
	if err := a.issueRestoreCredentials(ctx, inst.ProjectID, &target); err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	err = target.render(inst, recoveryTarget, clusterIdentity{image: image, altNames: altNames})
	if err != nil {
		return nil, fmt.Errorf("restore %s at tier %s: %w", inst.ProjectID, plan.Tier, err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)

	restored, err := a.runRestore(ctx, pc, inst, req, target)
	if err != nil {
		return nil, failRestore(ctx, pc, newProject, err)
	}
	return &domain.ProvisioningResponse{
		ProjectID:    restored.ProjectID,
		ProjectName:  restored.ProjectName,
		Status:       restored.Status,
		CurrentStage: restored.CurrentStage,
		Namespace:    restored.Namespace,
		Host:         restored.Host,
		Port:         restored.Port,
		DatabaseName: restored.DatabaseName,
		CreatedAt:    restored.CreatedAt,
	}, nil
}

// ensureTargetRecoverable refuses a point-in-time target whose WAL is not
// archived, so a recovery cannot end before it.
func (a *K8sBackupAdapter) ensureTargetRecoverable(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	if req.TargetTime == nil {
		return nil
	}
	if a.targetGuard == nil {
		return ErrRestoreTargetGuardNotConfigured
	}
	return a.targetGuard.EnsureRecoverable(ctx, inst, req.TargetTime.Time)
}

// issueRestoreCredentials mints, before anything is created, what the
// restored namespace will hold: the source read-only, its own prefix
// read-write when its plan backs it up.
func (a *K8sBackupAdapter) issueRestoreCredentials(ctx context.Context, sourceID string, target *restoreTarget) error {
	if a.creds == nil {
		return ErrBackupCredentialsNotConfigured
	}
	source, err := a.creds.ForRestoreSource(ctx, target.store, sourceID)
	if err != nil {
		return err
	}
	target.sourceCreds = source
	if backup := target.plan.Backup; backup != nil && backup.Enabled {
		own, err := a.creds.ForNewProject(ctx, target.store, target.project)
		if err != nil {
			return err
		}
		target.ownCreds = own
	}
	return nil
}

// restoreTarget is where a recovery lands and what it runs on. stores are
// applied before cluster, which reads through them.
type restoreTarget struct {
	// store is the platform's; only its location is rendered.
	store *domain.S3Credentials
	// sourceCreds and ownCreds are what the namespace receives.
	sourceCreds *domain.S3Credentials
	ownCreds    *domain.S3Credentials
	plan        RestorePlan
	stores      []*unstructured.Unstructured
	cluster     *unstructured.Unstructured
	project     string
	namespace   string
}

// clusterIdentity is what the restored cluster runs and is known by.
type clusterIdentity struct {
	image    string
	altNames []string
}

// render builds the cluster a new project with the source's settings and the
// plan's tier would get, bootstrapped from the source's backups, and the
// stores it reads and writes. Its own backups go to the store it is restored
// from, under its own prefix, with the credentials the restore puts in the
// namespace.
func (target *restoreTarget) render(src *domain.DatabaseInstance, recoveryTarget map[string]interface{}, id clusterIdentity) error {
	cluster := k8s.PostgreSQLClusterOpts{
		ProjectID:         target.project,
		Namespace:         target.namespace,
		Tier:              target.plan.Config,
		StorageClass:      src.StorageClass,
		ImageName:         id.image,
		DatabaseName:      src.DatabaseName,
		MasterUsername:    src.Username,
		Parameters:        src.Parameters,
		ServerAltDNSNames: id.altNames,
		// The same DocumentDB cluster creation builds: preload, pg_cron and
		// the gateway plugin are fixed when a cluster is created.
		DocumentDB:             src.DocumentDB,
		DocumentDBGatewayImage: config.DocumentDBGatewayImage(),
	}
	if backup := target.plan.Backup; backup != nil && backup.Enabled {
		if backup.Schedule == "" {
			return ErrRestoreBackupUnscheduled
		}
		cluster.Backup = &k8s.BackupOpts{
			Schedule: backup.Schedule, RetentionDays: backup.Retention,
			EndpointURL: target.store.Endpoint, Bucket: target.store.Bucket, SecretName: k8s.BackupCredentialsSecretName,
		}
	}
	opts := k8s.RestoreClusterOpts{
		Cluster:         cluster,
		SourceProjectID: src.ProjectID,
		Store:           k8s.ObjectStoreOpts{EndpointURL: target.store.Endpoint, Bucket: target.store.Bucket, SecretName: k8s.RecoverySourceCredentialsSecretName},
		RecoveryTarget:  recoveryTarget,
	}
	source, err := k8s.BuildRecoverySourceObjectStore(opts)
	if err != nil {
		return err
	}
	target.stores = []*unstructured.Unstructured{source}
	if cluster.Backup != nil {
		own, err := k8s.BuildBackupObjectStore(target.project, target.namespace, cluster.Backup.Store(), cluster.Backup.RetentionDays)
		if err != nil {
			return err
		}
		target.stores = append(target.stores, own)
	}
	target.cluster, err = k8s.BuildRestoreCluster(opts)
	return err
}

// runRestore drives the recovery to a project that has been proved usable.
// Every failure is returned so Restore can compensate through pc.
func (a *K8sBackupAdapter) runRestore(
	ctx context.Context,
	pc *provisioner.ProvisionContext,
	inst *domain.DatabaseInstance,
	req domain.RestoreRequest,
	target restoreTarget,
) (*domain.DatabaseInstance, error) {
	if err := a.createRestoreCluster(ctx, pc, inst, req, target); err != nil {
		return nil, err
	}
	if err := a.waitForRecoveredCluster(ctx, target.namespace, target.project); err != nil {
		return nil, err
	}
	// Inside the namespace, so its deletion compensates it.
	if err := provisioner.EnsureDocumentDBService(ctx, a.k8sClient, target.namespace, target.project, inst.DocumentDB); err != nil {
		return nil, err
	}
	if err := a.scheduleBackups(ctx, target); err != nil {
		return nil, err
	}
	restored := a.restoredInstance(ctx, inst, req, target)
	opts, err := ownerCredential(restored)
	if err != nil {
		return nil, err
	}
	if err := registerVerifiedProject(ctx, pc, a.registrar, a.instances, a.probe, restored, opts); err != nil {
		return nil, err
	}
	return restored, nil
}

// ownerCredential settles the restored project's owner password: the
// recovered cluster's own, forced onto the owner role. The source's password
// still opens the source, so it must never open the copy; without a
// credential of its own the restore is refused.
func ownerCredential(restored *domain.DatabaseInstance) (RegistrationOptions, error) {
	if restored.Password == "" {
		return RegistrationOptions{}, ErrRestoreOwnerCredentialMissing
	}
	return RegistrationOptions{ResetRolePasswords: true, ResetAdminPassword: true}, nil
}

// createRestoreCluster creates the target namespace, the object-store secret
// and the recovery-bootstrapped CNPG Cluster.
func (a *K8sBackupAdapter) createRestoreCluster(ctx context.Context, pc *provisioner.ProvisionContext, inst *domain.DatabaseInstance, req domain.RestoreRequest, target restoreTarget) error {
	newNamespace := target.namespace
	if err := a.k8sClient.CreateProjectNamespace(ctx, newNamespace, inst.OrgID); err != nil {
		return fmt.Errorf("create restore namespace: %w", err)
	}
	// Deleting the namespace removes the object-store secret with it, so the
	// secret needs no compensation of its own.
	pc.RegisterCleanup("delete restore namespace", func(ctx context.Context) error {
		return a.k8sClient.DeleteNamespace(ctx, newNamespace)
	})
	if err := a.writeCredentials(ctx, newNamespace, k8s.RecoverySourceCredentialsSecretName, target.sourceCreds); err != nil {
		return err
	}
	if target.ownCreds != nil {
		if err := a.writeCredentials(ctx, newNamespace, k8s.BackupCredentialsSecretName, target.ownCreds); err != nil {
			return err
		}
	}
	// Before the cluster: the gateway's env reads it, and a pod whose env
	// cannot resolve does not start.
	if err := provisioner.EnsureDocumentDBCredential(ctx, a.k8sClient, newNamespace, target.project, inst.DocumentDB); err != nil {
		return err
	}
	for _, store := range target.stores {
		if err := a.k8sClient.ApplyCRD(ctx, k8s.ObjectStoreGVR, newNamespace, store); err != nil {
			return fmt.Errorf("apply restore object store %s: %w", store.GetName(), err)
		}
	}
	clusterName := req.TargetProjectID + postgresClusterSuffix
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, target.cluster); err != nil {
		return fmt.Errorf("apply restore CRD: %w", err)
	}
	pc.RegisterCleanup("delete restore cluster", func(ctx context.Context) error {
		return a.k8sClient.DeleteCRD(ctx, k8s.CNPGClusterGVR, newNamespace, clusterName)
	})
	return nil
}

func (a *K8sBackupAdapter) writeCredentials(ctx context.Context, namespace, name string, creds *domain.S3Credentials) error {
	data, err := k8s.BackupCredentialsSecretData(creds)
	if err != nil {
		return err
	}
	if err := a.k8sClient.CreateSecret(ctx, namespace, name, data); err != nil {
		return fmt.Errorf("create restore credentials secret %s: %w", name, err)
	}
	return nil
}

// scheduleBackups schedules the restored project's backups as the provisioner
// does a new project's, once its cluster is up. The namespace's deletion takes
// the schedule with it, so it needs no compensation of its own.
func (a *K8sBackupAdapter) scheduleBackups(ctx context.Context, target restoreTarget) error {
	backup := target.plan.Backup
	if backup == nil || !backup.Enabled {
		return nil
	}
	// The recovered data has no base backup under the new project's prefix, so
	// its WAL is unusable for recovery until one is taken.
	scheduled, err := k8s.BuildFirstScheduledBackup(target.project, target.namespace, backup.Schedule)
	if err != nil {
		return fmt.Errorf("schedule restored project backups: %w", err)
	}
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, target.namespace, scheduled); err != nil {
		return fmt.Errorf("schedule restored project backups: %w", err)
	}
	return nil
}

// waitForRecoveredCluster blocks until CNPG reports the recovered Cluster
// healthy with a ready primary. Nothing but the operator writes that status,
// so an operator that never reconciles — or never installed — can only end
// in the wait's timeout, never in a completed restore.
func (a *K8sBackupAdapter) waitForRecoveredCluster(ctx context.Context, namespace, projectID string) error {
	cluster := projectID + postgresClusterSuffix
	return a.poller.WaitUntilReady(ctx, "recovered cluster "+cluster, func(ctx context.Context) (bool, error) {
		obj, err := a.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster)
		if err != nil {
			// The Cluster may not be visible yet, or the apiserver may be
			// briefly unreachable. Neither proves recovery impossible, so
			// the budget decides — but the reason is never silent.
			log.Printf("restore %s: read cluster status: %v", projectID, err)
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if isUnrecoverableClusterPhase(phase) {
			return false, fmt.Errorf("cluster reports phase %q", phase)
		}
		primary, _, _ := unstructured.NestedString(obj.Object, "status", "currentPrimary")
		ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyInstances")
		if primary == "" || ready < 1 {
			return false, a.recoveryMissedTarget(ctx, namespace, cluster)
		}
		podReady, err := a.k8sClient.IsPodReady(ctx, namespace, primary)
		if err != nil {
			log.Printf("restore %s: read primary %s readiness: %v", projectID, primary, err)
			return false, nil
		}
		return podReady, nil
	})
}

// isUnrecoverableClusterPhase reports whether CNPG has given up on the
// Cluster. CNPG's status.phase is a human-readable sentence rather than an
// enum ("Cluster is in an unrecoverable state, needs manual intervention"),
// so the check is on the words it uses for a terminal state.
func isUnrecoverableClusterPhase(phase string) bool {
	lower := strings.ToLower(phase)
	return strings.Contains(lower, "unrecoverable") || strings.Contains(lower, "failed")
}

// restoredInstance builds the project row for the restored cluster. It
// inherits the source project's org and database name, and records the tier
// its cluster was sized by. Its owner credential is the recovered cluster's
// own CNPG secret; the password is left empty when there is none.
func (a *K8sBackupAdapter) restoredInstance(ctx context.Context, src *domain.DatabaseInstance, req domain.RestoreRequest, target restoreTarget) *domain.DatabaseInstance {
	newProject, newNamespace := target.project, target.namespace
	dbName := src.DatabaseName
	if dbName == "" {
		dbName = defaultRestoreDatabase
	}
	username, password := src.Username, ""
	if secret, err := a.k8sClient.GetSecret(ctx, newNamespace, newProject+"-postgres-app"); err == nil {
		if u := string(secret["username"]); u != "" {
			username, password = u, string(secret["password"])
		}
	}
	port := 5432
	now := &domain.FlexTime{Time: time.Now()}
	restored := &domain.DatabaseInstance{
		ProjectID:             newProject,
		ProjectName:           req.NewProjectName,
		OrgID:                 src.OrgID,
		OwnerID:               src.OwnerID,
		DBType:                src.DBType,
		Tier:                  target.plan.Tier,
		DeploymentMode:        domain.ModeK8s,
		Namespace:             newNamespace,
		Host:                  fmt.Sprintf("%s-postgres-rw.%s.svc.cluster.local", newProject, newNamespace),
		ReadOnlyHost:          fmt.Sprintf("%s-postgres-r.%s.svc.cluster.local", newProject, newNamespace),
		Port:                  &port,
		DatabaseName:          dbName,
		Username:              username,
		Password:              password,
		SSLMode:               src.SSLMode,
		PostgresVersion:       src.PostgresVersion,
		DocumentDB:            src.DocumentDB,
		StorageClass:          src.StorageClass,
		Parameters:            maps.Clone(src.Parameters),
		RestoredFromProjectID: src.ProjectID,
		RestoredFromBackupID:  req.BackupID,
		CreatedAt:             now,
	}
	if backup := target.plan.Backup; backup != nil {
		restored.BackupEnabled = boolPtr(backup.Enabled)
		restored.BackupSchedule = backup.Schedule
		restored.BackupRetentionDays = intPtr(backup.Retention)
	}
	return restored
}

// backupStorage resolves the configured object store, tolerating a nil
// source (legacy wiring without backup config).
func (a *K8sBackupAdapter) backupStorage() (*domain.S3Credentials, bool) {
	if a.storage == nil {
		return nil, false
	}
	return a.storage.BackupStorage()
}
