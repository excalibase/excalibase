package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	// context needed for syncBackupStatus
	_ "context"
)

type BackupService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
}

func NewBackupService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *BackupService {
	return &BackupService{store: store, k8sClient: client, storagePath: storagePath}
}

func (s *BackupService) TriggerManualBackup(ctx context.Context, projectID string) (map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	backupName := fmt.Sprintf("%s-backup-%s", projectID, time.Now().Format("20060102-150405"))
	backup := k8s.BuildManualBackup(projectID, inst.Namespace, backupName)
	if err := s.k8sClient.ApplyCRD(ctx, k8s.CNPGBackupGVR, inst.Namespace, backup); err != nil {
		return nil, fmt.Errorf("trigger backup: %w", err)
	}

	// Save backup record
	record := map[string]interface{}{
		"id":        backupName,
		"projectId": projectID,
		"timestamp": time.Now().Format(time.RFC3339),
		"type":      "MANUAL",
		"status":    "IN_PROGRESS",
	}
	s.saveBackupRecord(projectID, backupName, record)

	return record, nil
}

func (s *BackupService) ListBackups(projectID string) ([]map[string]interface{}, error) {
	// Also check K8s for actual backup status and update stored records
	if inst, _ := s.store.FindByProjectID(projectID); inst != nil {
		s.syncBackupStatus(context.Background(), inst.Namespace, projectID)
	}

	dir := filepath.Join(s.storagePath, "projects", projectID, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []map[string]interface{}{}, nil
	}

	result := make([]map[string]interface{}, 0)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var record map[string]interface{}
		if json.Unmarshal(data, &record) == nil {
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *BackupService) RestoreFromBackup(ctx context.Context, projectID string, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	newProject := req.GetNewProject()
	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, newProject)

	// Create namespace
	if err := s.k8sClient.CreateNamespace(ctx, newNamespace); err != nil {
		return nil, fmt.Errorf("create restore namespace: %w", err)
	}

	// Create S3 credentials
	s.k8sClient.CreateSecret(ctx, newNamespace, "backup-s3-creds", map[string][]byte{
		"ACCESS_KEY_ID":     []byte("test"),
		"ACCESS_SECRET_KEY": []byte("test"),
	})

	// Build restore cluster CRD
	restoreSpec := map[string]interface{}{
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
						"destinationPath": fmt.Sprintf("s3://postgres-backups/%s", projectID),
						"endpointURL":     "http://localstack.localstack.svc.cluster.local:4566",
						"s3Credentials": map[string]interface{}{
							"accessKeyId":     map[string]interface{}{"name": "backup-s3-creds", "key": "ACCESS_KEY_ID"},
							"secretAccessKey": map[string]interface{}{"name": "backup-s3-creds", "key": "ACCESS_SECRET_KEY"},
						},
						"wal": map[string]interface{}{"maxParallel": int64(8)},
					},
				},
			},
		},
	}

	// PITR: add targetTime
	if req.TargetTime != nil {
		recovery := restoreSpec["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
		recovery["recoveryTarget"] = map[string]interface{}{
			"targetTime": req.TargetTime.Time.Format("2006-01-02T15:04:05Z"),
		}
	}

	// Apply restore cluster CRD
	restoreObj := &unstructured.Unstructured{Object: restoreSpec}
	if err := s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, restoreObj); err != nil {
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

func (s *BackupService) syncBackupStatus(ctx context.Context, namespace, projectID string) {
	dir := filepath.Join(s.storagePath, "projects", projectID, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var record map[string]interface{}
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		if record["status"] == "IN_PROGRESS" {
			// Check K8s backup status
			backupName, _ := record["id"].(string)
			if backupName == "" {
				continue
			}
			obj, err := s.k8sClient.GetCRD(ctx, k8s.CNPGBackupGVR, namespace, backupName)
			if err == nil {
				status, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
				if status == "completed" {
					record["status"] = "COMPLETED"
					updated, err := json.MarshalIndent(record, "", "  ")
					if err != nil {
						log.Printf("WARN: marshal backup record %s: %v", e.Name(), err)
						continue
					}
					if err := os.WriteFile(filepath.Join(dir, e.Name()), updated, 0644); err != nil {
						log.Printf("WARN: write %s: %v", filepath.Join(dir, e.Name()), err)
					}
				} else if status == "failed" {
					record["status"] = "FAILED"
					updated, err := json.MarshalIndent(record, "", "  ")
					if err != nil {
						log.Printf("WARN: marshal backup record %s: %v", e.Name(), err)
						continue
					}
					if err := os.WriteFile(filepath.Join(dir, e.Name()), updated, 0644); err != nil {
						log.Printf("WARN: write %s: %v", filepath.Join(dir, e.Name()), err)
					}
				}
			}
		}
	}
}

func (s *BackupService) GetInstance(projectID string) (*domain.DatabaseInstance, error) {
	return s.store.FindByProjectID(projectID)
}

func (s *BackupService) saveBackupRecord(projectID, backupName string, record map[string]interface{}) {
	dir := filepath.Join(s.storagePath, "projects", projectID, "backups")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("WARN: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal backup record %s: %v", backupName, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, backupName+".json"), data, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, backupName+".json"), err)
	}
}
