package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"time"
)

// ScaleTier changes the instance count and resources by patching the CNPG Cluster CRD.
func (s *ProvisioningService) ScaleTier(ctx context.Context, projectID string, newTier domain.TierType, k8sClient k8s.KubeClient) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	tc, err := config.GetTierConfig(newTier)
	if err != nil {
		return err
	}

	// Get existing cluster CRD and update spec
	clusterName := projectID + "-postgres"
	existing, err := k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster CRD: %w", err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	spec["instances"] = int64(tc.Instances)
	spec["resources"] = map[string]interface{}{
		"requests": map[string]interface{}{"memory": tc.Memory, "cpu": tc.CPU},
		"limits":   map[string]interface{}{"memory": tc.Memory, "cpu": tc.CPU},
	}

	if err := k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing); err != nil {
		return fmt.Errorf("patch cluster CRD: %w", err)
	}

	inst.Tier = newTier
	return s.store.Save(inst)
}

// ResizeStorage patches the CNPG Cluster CRD storage size.
func (s *ProvisioningService) ResizeStorage(ctx context.Context, projectID, newSize string, k8sClient k8s.KubeClient) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	clusterName := projectID + "-postgres"
	existing, err := k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster CRD: %w", err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	storage := spec["storage"].(map[string]interface{})
	storage["size"] = newSize

	return k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// UpgradeVersion patches the CNPG Cluster CRD imageName to trigger a rolling restart.
func (s *ProvisioningService) UpgradeVersion(ctx context.Context, projectID, newVersion string, k8sClient k8s.KubeClient) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	clusterName := projectID + "-postgres"
	existing, err := k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster CRD: %w", err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	spec["imageName"] = fmt.Sprintf("ghcr.io/cloudnative-pg/postgresql:%s", newVersion)

	return k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// CloneDatabase creates a new cluster using pg_basebackup from the source.
func (s *ProvisioningService) CloneDatabase(ctx context.Context, projectID string, req domain.CloneRequest, k8sClient k8s.KubeClient) (*domain.ProvisioningResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, req.NewProjectName)

	if err := k8sClient.CreateNamespace(ctx, newNamespace); err != nil {
		return nil, fmt.Errorf("create clone namespace: %w", err)
	}

	cloneObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      req.NewProjectName + "-postgres",
				"namespace": newNamespace,
			},
			"spec": map[string]interface{}{
				"instances": int64(1),
				"storage":   map[string]interface{}{"size": "5Gi"},
				"bootstrap": map[string]interface{}{
					"pg_basebackup": map[string]interface{}{
						"source": projectID + "-postgres",
					},
				},
				"externalClusters": []interface{}{
					map[string]interface{}{
						"name": projectID + "-postgres",
						"connectionParameters": map[string]interface{}{
							"host":   inst.Host,
							"dbname": inst.DatabaseName,
						},
					},
				},
			},
		},
	}

	if err := k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, cloneObj); err != nil {
		return nil, fmt.Errorf("apply clone CRD: %w", err)
	}

	now := &domain.FlexTime{Time: time.Now()}
	return &domain.ProvisioningResponse{
		ProjectID:    req.NewProjectName,
		Status:       "CLONING",
		CurrentStage: domain.StageWaitingForReady,
		Namespace:    newNamespace,
		CreatedAt:    now,
	}, nil
}

// GetLogs tails logs from the primary pod.
func (s *ProvisioningService) GetLogs(ctx context.Context, projectID string, lines int, k8sClient k8s.KubeClient) (string, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return "", err
	}

	pod := projectID + "-postgres-1"
	return k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"sh", "-c", fmt.Sprintf("tail -%d /controller/log/postgres.csv", lines)})
}

// RotateCredentials generates a new password and updates the database.
func (s *ProvisioningService) RotateCredentials(ctx context.Context, projectID string, k8sClient k8s.KubeClient) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	newPassword := generatePassword(48)
	pod := projectID + "-postgres-1"
	sql := fmt.Sprintf("ALTER USER %s PASSWORD '%s'", inst.Username, newPassword)

	_, err = k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-c", sql})
	if err != nil {
		return nil, fmt.Errorf("rotate password: %w", err)
	}

	inst.Password = newPassword
	s.store.Save(inst)

	return s.GetCredentials(projectID)
}

// SetMaintenanceWindow sets the maintenance window config.
func (s *ProvisioningService) SetMaintenanceWindow(projectID string, cfg domain.MaintenanceWindowConfig) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}
	inst.MaintenanceWindow = cfg.Window
	inst.MaintenanceWindowDurationMinutes = &cfg.DurationMinutes
	inst.AutoMinorVersionUpgrade = &cfg.AutoUpgrade
	return s.store.Save(inst)
}

// GetMaintenanceWindow returns the maintenance window config.
func (s *ProvisioningService) GetMaintenanceWindow(projectID string) (*domain.MaintenanceWindowConfig, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	dur := 0
	if inst.MaintenanceWindowDurationMinutes != nil {
		dur = *inst.MaintenanceWindowDurationMinutes
	}
	autoUpgrade := false
	if inst.AutoMinorVersionUpgrade != nil {
		autoUpgrade = *inst.AutoMinorVersionUpgrade
	}
	return &domain.MaintenanceWindowConfig{
		Window:          inst.MaintenanceWindow,
		DurationMinutes: dur,
		AutoUpgrade:     autoUpgrade,
	}, nil
}

// UpdateParameters patches PostgreSQL parameters on the CNPG Cluster CRD (triggers rolling restart).
func (s *ProvisioningService) UpdateParameters(ctx context.Context, projectID string, params map[string]string, k8sClient k8s.KubeClient) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	clusterName := projectID + "-postgres"
	existing, err := k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster CRD: %w", err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	pg := spec["postgresql"].(map[string]interface{})
	pgParams, ok := pg["parameters"].(map[string]interface{})
	if !ok {
		pgParams = make(map[string]interface{})
		pg["parameters"] = pgParams
	}

	for k, v := range params {
		pgParams[k] = v
	}

	return k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// EnablePooler creates a CNPG Pooler CRD (PgBouncer) for connection pooling.
func (s *ProvisioningService) EnablePooler(ctx context.Context, projectID string, settings domain.PoolerSettings, k8sClient k8s.KubeClient) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	poolMode := "transaction"
	if settings.PoolMode != "" {
		poolMode = settings.PoolMode
	}
	poolSize := int64(20)
	if settings.PoolSize > 0 {
		poolSize = int64(settings.PoolSize)
	}

	poolerObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Pooler",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-pooler",
				"namespace": inst.Namespace,
			},
			"spec": map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": projectID + "-postgres",
				},
				"instances": int64(1),
				"type":      "rw",
				"pgbouncer": map[string]interface{}{
					"poolMode": poolMode,
					"parameters": map[string]interface{}{
						"default_pool_size": fmt.Sprintf("%d", poolSize),
					},
				},
			},
		},
	}

	poolerGVR := k8s.CNPGClusterGVR // same group, different resource
	poolerGVR.Resource = "poolers"

	if err := k8sClient.ApplyCRD(ctx, poolerGVR, inst.Namespace, poolerObj); err != nil {
		return fmt.Errorf("apply pooler CRD: %w", err)
	}

	enabled := true
	inst.PoolerEnabled = &enabled
	inst.PoolerHost = fmt.Sprintf("%s-postgres-pooler.%s.svc.cluster.local", projectID, inst.Namespace)
	return s.store.Save(inst)
}

func generatePassword(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:length]
}
