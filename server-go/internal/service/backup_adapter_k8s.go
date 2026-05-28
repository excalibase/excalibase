package service

import (
	"context"
	"encoding/json"
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
}

func NewK8sBackupAdapter(client k8s.KubeClient, storagePath string) *K8sBackupAdapter {
	return &K8sBackupAdapter{k8sClient: client, storagePath: storagePath}
}

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

func (a *K8sBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	newProject := req.GetNewProject()
	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, newProject)

	if err := a.k8sClient.CreateNamespace(ctx, newNamespace); err != nil {
		return nil, fmt.Errorf("create restore namespace: %w", err)
	}

	// Plant the real R2/S3 credentials from the same env the backup-write
	// path reads (set by the chart from the r2-creds Secret). Falls back to
	// "test"/"test" so the in-cluster mock-floci dev flow still works.
	accessKey := envOrFallback("R2_ACCESS_KEY_ID", "BACKUP_DEFAULT_ACCESS_KEY_ID", "test")
	secretKey := envOrFallback("R2_SECRET_ACCESS_KEY", "BACKUP_DEFAULT_SECRET_ACCESS_KEY", "test")
	a.k8sClient.CreateSecret(ctx, newNamespace, s3CredsKey, map[string][]byte{
		"ACCESS_KEY_ID":     []byte(accessKey),
		"ACCESS_SECRET_KEY": []byte(secretKey),
	})

	// Bucket: mirror main.go's BackupDefaults — explicit BACKUP_DEFAULT_BUCKET
	// wins, otherwise the hard default "excalibase-backups". R2_BUCKET is
	// the STORAGE bucket (a different bucket from where Barman writes the
	// CNPG backups) so do NOT use it here.
	bucket := envOrFallback("BACKUP_DEFAULT_BUCKET", "excalibase-backups")
	endpoint := envOrFallback("BACKUP_DEFAULT_ENDPOINT", "R2_ENDPOINT", "http://localstack.localstack.svc.cluster.local:4566")

	restoreSpec := buildRestoreSpec(inst.ProjectID, newProject, newNamespace, req, bucket, endpoint)
	restoreObj := &unstructured.Unstructured{Object: restoreSpec}
	if err := a.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, restoreObj); err != nil {
		return nil, fmt.Errorf("apply restore CRD: %w", err)
	}

	now := &domain.FlexTime{Time: time.Now()}
	return &domain.ProvisioningResponse{
		ProjectID:    newProject,
		Status:       "RESTORING",
		CurrentStage: domain.StageWaitingForReady,
		Namespace:    newNamespace,
		CreatedAt:    now,
	}, nil
}

// envOrFallback returns the first non-empty value among the listed env
// vars; if none are set it returns fallback.
func envOrFallback(envs ...string) string {
	if len(envs) == 0 {
		return ""
	}
	fallback := envs[len(envs)-1]
	for _, e := range envs[:len(envs)-1] {
		if v := os.Getenv(e); v != "" {
			return v
		}
	}
	return fallback
}

// buildRestoreSpec produces the recovery-bootstrap CNPG cluster CRD.
// PITR fields are added when present in req.
func buildRestoreSpec(sourceProject, newProject, newNamespace string, req domain.RestoreRequest, bucket, endpoint string) map[string]interface{} {
	spec := map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]interface{}{
			"name":      newProject + "-postgres",
			"namespace": newNamespace,
		},
		"spec": map[string]interface{}{
			"instances": int64(1),
			"storage":   map[string]interface{}{"size": "5Gi"},
			"bootstrap": map[string]interface{}{
				"recovery": map[string]interface{}{
					"source": "clusterBackup",
				},
			},
			"externalClusters": []interface{}{
				map[string]interface{}{
					"name": "clusterBackup",
					"barmanObjectStore": map[string]interface{}{
						"serverName":      "cloud",
						"destinationPath": fmt.Sprintf("s3://%s/%s", bucket, sourceProject),
						"endpointURL":     endpoint,
						"s3Credentials": map[string]interface{}{
							"accessKeyId":     map[string]interface{}{"name": s3CredsKey, "key": "ACCESS_KEY_ID"},
							"secretAccessKey": map[string]interface{}{"name": s3CredsKey, "key": "ACCESS_SECRET_KEY"},
						},
						"wal": map[string]interface{}{"maxParallel": int64(8)},
					},
				},
			},
		},
	}

	if target := req.RecoveryTarget(); target != nil {
		recovery := spec["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
		recovery["recoveryTarget"] = target
	}

	return spec
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
