package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/security"
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
	// registrar finishes a restore the way a provision ends: roles, vault,
	// instance row, PgDog, events. Restore refuses to run without it —
	// a restored project nobody registered is invisible to the API.
	registrar ProjectRegistrar
	// readyTimeout / readyPoll bound the wait for the restored primary.
	// A restore replays WAL, so the default is generous.
	readyTimeout time.Duration
	readyPoll    time.Duration
}

// ErrProjectRegistrarNotConfigured is returned when a restore would produce a
// database the platform cannot register as a project.
var ErrProjectRegistrarNotConfigured = errors.New("restore: project registrar not configured")

const (
	defaultRestoreReadyTimeout = 15 * time.Minute
	defaultRestoreReadyPoll    = 5 * time.Second
)

// NewK8sBackupAdapter wires the CNPG adapter. storage must be the same
// source the provisioner writes backups with (ProvisioningService
// implements it); nil means restores fail with ErrBackupStorageNotConfigured.
func NewK8sBackupAdapter(client k8s.KubeClient, storagePath string, storage BackupStorageSource) *K8sBackupAdapter {
	return &K8sBackupAdapter{
		k8sClient:    client,
		storagePath:  storagePath,
		storage:      storage,
		readyTimeout: defaultRestoreReadyTimeout,
		readyPoll:    defaultRestoreReadyPoll,
	}
}

// SetProjectRegistrar wires the shared registration path. Called from main.go
// once the provisioning service exists.
func (a *K8sBackupAdapter) SetProjectRegistrar(r ProjectRegistrar) { a.registrar = r }

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
// Failure semantics once the Cluster exists: the namespace is deliberately
// left in place for inspection and no instance row is written, so the operator
// sees a FAILED restore job naming the step plus a live namespace to debug —
// never a half-registered project the API would serve.
func (a *K8sBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	store, ok := a.backupStorage()
	if !ok {
		return nil, ErrBackupStorageNotConfigured
	}
	if a.registrar == nil {
		return nil, ErrProjectRegistrarNotConfigured
	}
	newProject := req.GetNewProject()
	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, newProject)

	if err := a.createRestoreCluster(ctx, inst, req, store, newNamespace); err != nil {
		return nil, err
	}
	if err := a.waitForRestoredPrimary(ctx, newNamespace, newProject); err != nil {
		return nil, err
	}

	restored := a.restoredInstance(ctx, inst, req, newProject, newNamespace)
	if err := a.registrar.RegisterProject(ctx, restored, RegistrationOptions{ResetRolePasswords: true}); err != nil {
		return nil, fmt.Errorf("register restored project: %w", err)
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

// createRestoreCluster creates the target namespace, the object-store secret
// and the recovery-bootstrapped CNPG Cluster.
func (a *K8sBackupAdapter) createRestoreCluster(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest, store *domain.S3Credentials, newNamespace string) error {
	if err := a.k8sClient.CreateNamespace(ctx, newNamespace); err != nil {
		return fmt.Errorf("create restore namespace: %w", err)
	}
	if err := a.k8sClient.CreateSecret(ctx, newNamespace, s3CredsKey, map[string][]byte{
		"ACCESS_KEY_ID":     []byte(store.AccessKeyID),
		"ACCESS_SECRET_KEY": []byte(store.SecretAccessKey),
	}); err != nil {
		return fmt.Errorf("create restore credentials secret: %w", err)
	}
	restoreObj := k8s.BuildRestoreCluster(k8s.RestoreClusterOpts{
		SourceProjectID: inst.ProjectID,
		NewProjectID:    req.GetNewProject(),
		Namespace:       newNamespace,
		Store:           k8s.ObjectStoreOpts{EndpointURL: store.Endpoint, Bucket: store.Bucket, SecretName: s3CredsKey},
		RecoveryTarget:  req.RecoveryTarget(),
	})
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, restoreObj); err != nil {
		return fmt.Errorf("apply restore CRD: %w", err)
	}
	return nil
}

// waitForRestoredPrimary blocks until CNPG reports the recovered primary pod
// Ready — which it only does once WAL replay has finished and postgres is
// accepting connections, so role SQL can run straight after.
func (a *K8sBackupAdapter) waitForRestoredPrimary(ctx context.Context, namespace, projectID string) error {
	pod := projectID + primaryPodSuffix
	deadline := time.Now().Add(a.readyTimeout)
	for time.Now().Before(deadline) {
		if ready, err := a.k8sClient.IsPodReady(ctx, namespace, pod); err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.readyPoll):
		}
	}
	return fmt.Errorf("restored primary %s did not become ready within %v (namespace kept for inspection)", pod, a.readyTimeout)
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
		ProjectName:           newProject,
		OrgID:                 src.OrgID,
		OwnerID:               src.OwnerID,
		DBType:                src.DBType,
		Tier:                  src.Tier,
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
