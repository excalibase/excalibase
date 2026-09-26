package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// K8sBackupAdapter wraps the CNPG flow. Every method here was lifted
// verbatim from the pre-Phase-1 BackupService body — keeping behaviour
// pinned so existing CNPG tests pass unchanged.
type K8sBackupAdapter struct {
	k8sClient   k8s.KubeClient
	storagePath string
	// storage is the object store backups are written to; restores read
	// from it. Resolved per call so vault rotation is picked up.
	storage BackupStorageSource
	// instances is consulted before a restore creates anything, so a target
	// id that is already registered is refused rather than built over.
	instances storage.InstanceStore
	// registrar finishes a restore the way a provision ends: roles, vault,
	// instance row, PgDog, events. Restore refuses to run without it —
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
}

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
func NewK8sBackupAdapter(client k8s.KubeClient, storagePath string, storage BackupStorageSource) *K8sBackupAdapter {
	return &K8sBackupAdapter{
		k8sClient:   client,
		storagePath: storagePath,
		storage:     storage,
		poller:      provisioner.NewPoller(defaultRestoreReadyPoll, defaultRestoreReadyTimeout),
	}
}

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

	ref := BackupRef{
		ID:        backupName,
		ProjectID: inst.ProjectID,
		Type:      "MANUAL",
		Status:    "IN_PROGRESS",
		StartedAt: time.Now().Format(time.RFC3339),
	}
	a.saveBackupRecord(inst.ProjectID, backupName, ref)
	return ref, nil
}

// BackupsConfigured reports whether an object store is wired. Without one
// CNPG has nowhere to put a backup.
func (a *K8sBackupAdapter) BackupsConfigured() bool {
	_, ok := a.backupStorage()
	return ok
}

func (a *K8sBackupAdapter) List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error) {
	a.syncBackupStatus(ctx, inst.Namespace, inst.ProjectID)

	dir := filepath.Join(a.storagePath, "projects", inst.ProjectID, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupRef{}, nil
	}

	result := make([]BackupRef, 0, len(entries))
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		var ref BackupRef
		if json.Unmarshal(data, &ref) == nil {
			// IDOR safety: even if a malicious file were dropped on disk
			// with another projectId, override with the trusted one from
			// the loaded instance.
			ref.ProjectID = inst.ProjectID
			result = append(result, ref)
		}
	}
	return result, nil
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
	if inst.DocumentDB {
		return nil, ErrDocumentDBRestoreNotSupported
	}
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
	if inst.OrgID == "" {
		return nil, fmt.Errorf("restore source %s: %w", inst.ProjectID, k8s.ErrProjectOrgRequired)
	}
	image, err := config.PostgresImage(inst.PostgresVersion)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	newProject := req.TargetProjectID
	altNames, err := provisioner.PublicServerNames(newProject, a.publicDomainSuffix)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	if err := assertProjectIDAvailable(a.instances, newProject); err != nil {
		return nil, err
	}
	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, newProject)
	pc := provisioner.NewProvisionContext(nil, nil)

	restored, err := a.runRestore(ctx, pc, inst, req, restoreTarget{
		store: store, image: image, altNames: altNames, project: newProject, namespace: newNamespace,
	})
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

// restoreTarget is where a recovery lands and what it runs on.
type restoreTarget struct {
	store     *domain.S3Credentials
	image     string
	altNames  []string
	project   string
	namespace string
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
	restored := a.restoredInstance(ctx, inst, req, target.project, target.namespace)
	if err := registerVerifiedProject(ctx, pc, a.registrar, a.instances, a.probe, restored,
		RegistrationOptions{ResetRolePasswords: true}); err != nil {
		return nil, err
	}
	return restored, nil
}

// createRestoreCluster creates the target namespace, the object-store secret
// and the recovery-bootstrapped CNPG Cluster.
func (a *K8sBackupAdapter) createRestoreCluster(ctx context.Context, pc *provisioner.ProvisionContext, inst *domain.DatabaseInstance, req domain.RestoreRequest, target restoreTarget) error {
	store, newNamespace := target.store, target.namespace
	if err := a.k8sClient.CreateProjectNamespace(ctx, newNamespace, inst.OrgID); err != nil {
		return fmt.Errorf("create restore namespace: %w", err)
	}
	// Deleting the namespace removes the object-store secret with it, so the
	// secret needs no compensation of its own.
	pc.RegisterCleanup("delete restore namespace", func(ctx context.Context) error {
		return a.k8sClient.DeleteNamespace(ctx, newNamespace)
	})
	if err := a.k8sClient.CreateSecret(ctx, newNamespace, s3CredsKey, map[string][]byte{
		"ACCESS_KEY_ID":     []byte(store.AccessKeyID),
		"ACCESS_SECRET_KEY": []byte(store.SecretAccessKey),
	}); err != nil {
		return fmt.Errorf("create restore credentials secret: %w", err)
	}
	restoreObj := k8s.BuildRestoreCluster(k8s.RestoreClusterOpts{
		SourceProjectID:   inst.ProjectID,
		NewProjectID:      req.TargetProjectID,
		Namespace:         newNamespace,
		Store:             k8s.ObjectStoreOpts{EndpointURL: store.Endpoint, Bucket: store.Bucket, SecretName: s3CredsKey},
		RecoveryTarget:    req.RecoveryTarget(),
		ImageName:         target.image,
		ServerAltDNSNames: target.altNames,
	})
	clusterName := req.TargetProjectID + postgresClusterSuffix
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, restoreObj); err != nil {
		return fmt.Errorf("apply restore CRD: %w", err)
	}
	pc.RegisterCleanup("delete restore cluster", func(ctx context.Context) error {
		return a.k8sClient.DeleteCRD(ctx, k8s.CNPGClusterGVR, newNamespace, clusterName)
	})
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
			return false, nil
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
// inherits the source project's org, tier and database name; credentials come
// from the CNPG-managed secret when the recovered cluster has one, otherwise
// from the source (the restored database is a copy, so the source's owner
// credentials are valid in it).
func (a *K8sBackupAdapter) restoredInstance(ctx context.Context, src *domain.DatabaseInstance, req domain.RestoreRequest, newProject, newNamespace string) *domain.DatabaseInstance {
	dbName := src.DatabaseName
	if dbName == "" {
		dbName = defaultRestoreDatabase
	}
	username, password := src.Username, src.Password
	if secret, err := a.k8sClient.GetSecret(ctx, newNamespace, newProject+"-postgres-app"); err == nil {
		if u := string(secret["username"]); u != "" {
			username, password = u, string(secret["password"])
		}
	}
	port := 5432
	now := &domain.FlexTime{Time: time.Now()}
	return &domain.DatabaseInstance{
		ProjectID:             newProject,
		ProjectName:           req.NewProjectName,
		OrgID:                 src.OrgID,
		OwnerID:               src.OwnerID,
		DBType:                src.DBType,
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
		RestoredFromProjectID: src.ProjectID,
		RestoredFromBackupID:  req.BackupID,
		CreatedAt:             now,
	}
}

// backupStorage resolves the configured object store, tolerating a nil
// source (legacy wiring without backup config).
func (a *K8sBackupAdapter) backupStorage() (*domain.S3Credentials, bool) {
	if a.storage == nil {
		return nil, false
	}
	return a.storage.BackupStorage()
}

func (a *K8sBackupAdapter) syncBackupStatus(ctx context.Context, namespace, projectID string) {
	dir := filepath.Join(a.storagePath, "projects", projectID, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		safePath := filepath.Join(dir, filepath.Base(e.Name()))
		a.syncBackupEntry(ctx, namespace, safePath)
	}
}

func (a *K8sBackupAdapter) syncBackupEntry(ctx context.Context, namespace, safePath string) {
	data, _ := os.ReadFile(safePath)
	var ref BackupRef
	if json.Unmarshal(data, &ref) != nil {
		return
	}
	if ref.Status != "IN_PROGRESS" || ref.ID == "" {
		return
	}
	obj, err := a.k8sClient.GetCRD(ctx, k8s.CNPGBackupGVR, namespace, ref.ID)
	if err != nil {
		return
	}
	status, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	newStatus := mapK8sBackupPhase(status)
	if newStatus == "" {
		return
	}
	ref.Status = newStatus
	if newStatus == "COMPLETED" || newStatus == "FAILED" {
		ref.FinishedAt = time.Now().Format(time.RFC3339)
	}
	a.writeBackupRecord(safePath, ref)
}

// mapK8sBackupPhase converts a CNPG backup phase to our status string.
// Returns "" when the phase requires no update.
func mapK8sBackupPhase(phase string) string {
	switch phase {
	case "completed":
		return "COMPLETED"
	case "failed":
		return "FAILED"
	default:
		return ""
	}
}

func (a *K8sBackupAdapter) writeBackupRecord(safePath string, ref BackupRef) {
	updated, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		log.Printf(warnMarshalFmt, safePath, err)
		return
	}
	if err := os.WriteFile(safePath, updated, 0644); err != nil {
		log.Printf(warnWriteFmt, safePath, err)
	}
}

func (a *K8sBackupAdapter) saveBackupRecord(projectID, backupName string, ref BackupRef) {
	dir := filepath.Join(a.storagePath, "projects", projectID, "backups")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("WARN: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		log.Printf(warnMarshalFmt, backupName, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, backupName+".json"), data, 0644); err != nil {
		log.Printf(warnWriteFmt, filepath.Join(dir, backupName+".json"), err)
	}
}
