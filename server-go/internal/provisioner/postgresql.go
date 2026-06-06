package provisioner

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const primaryPodSuffix = "-postgres-1"

const clusterNameSuffix = "-postgres"

// PostgreSQLProvisioner provisions PostgreSQL via CloudNativePG operator.
type PostgreSQLProvisioner struct {
	client           k8s.KubeClient
	watcherChartPath string
}

func NewPostgreSQLProvisioner(client k8s.KubeClient, watcherChartPath string) *PostgreSQLProvisioner {
	return &PostgreSQLProvisioner{client: client, watcherChartPath: watcherChartPath}
}

func (p *PostgreSQLProvisioner) SupportedType() domain.DatabaseType {
	return domain.PostgreSQL
}

func (p *PostgreSQLProvisioner) provisionNamespace(ctx context.Context, req domain.ProvisioningRequest, namespace string) error {
	labels := map[string]string{
		"excalibase.io/type": "project",
		"excalibase.io/org":  req.OrgID,
	}
	if err := p.client.CreateNamespaceWithLabels(ctx, namespace, labels); err != nil {
		return fmt.Errorf("create namespace: %w", err)
	}
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 != nil {
		if err := p.client.CreateSecret(ctx, namespace, "backup-s3-creds", map[string][]byte{
			"ACCESS_KEY_ID":     []byte(req.Backup.S3.AccessKeyID),
			"ACCESS_SECRET_KEY": []byte(req.Backup.S3.SecretAccessKey),
		}); err != nil {
			return fmt.Errorf("create backup secret: %w", err)
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) provisionCRD(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, projectID, namespace string) error {
	opts := k8s.PostgreSQLClusterOpts{
		ProjectID:       projectID,
		Namespace:       namespace,
		Tier:            tier,
		StorageClass:    req.StorageClass,
		PostgresVersion: req.PostgresVersion,
		DatabaseName:    req.DatabaseName,
		MasterUsername:  req.MasterUsername,
		Parameters:      req.Parameters,
		Tags:            req.Tags,
	}
	if req.Backup != nil && req.Backup.Enabled {
		opts.Backup = &k8s.BackupOpts{
			Schedule:      req.Backup.Schedule,
			RetentionDays: req.Backup.Retention,
		}
		if req.Backup.S3 != nil {
			opts.Backup.EndpointURL = req.Backup.S3.Endpoint
			opts.Backup.Bucket = req.Backup.S3.Bucket
		}
	}
	cluster := k8s.BuildPostgreSQLCluster(opts)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("deploy cluster CRD: %w", err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) waitForAllPods(ctx context.Context, tier config.TierConfig, projectID, namespace string) error {
	primaryPod := projectID + primaryPodSuffix
	if err := p.waitForPodReady(ctx, namespace, primaryPod, 5*time.Minute); err != nil {
		return fmt.Errorf("waiting for ready: %w", err)
	}
	for i := 2; i <= tier.Instances; i++ {
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		if err := p.waitForPodReady(ctx, namespace, pod, 3*time.Minute); err != nil {
			return fmt.Errorf("waiting for replica %d: %w", i, err)
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) provisionScheduledBackup(ctx context.Context, req domain.ProvisioningRequest, projectID, namespace string) error {
	schedule := req.Backup.Schedule
	if schedule == "" {
		schedule = "0 0 * * *"
	}
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup); err != nil {
		return fmt.Errorf("configure backup: %w", err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	projectID := req.ProjectName
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)

	cb(domain.StageValidating)

	// Stage 2: Namespace + optional backup secret
	cb(domain.StageNamespaceCreation)
	if err := p.provisionNamespace(ctx, req, namespace); err != nil {
		return nil, err
	}

	// Stage 3: CRD
	cb(domain.StageCRDDeployment)
	if err := p.provisionCRD(ctx, req, tier, projectID, namespace); err != nil {
		return nil, err
	}

	// Stage 4: Wait for pods
	cb(domain.StageWaitingForReady)
	if err := p.waitForAllPods(ctx, tier, projectID, namespace); err != nil {
		return nil, err
	}

	// Stage 5: Credentials
	cb(domain.StageCredentialGeneration)
	creds, err := p.extractCredentials(ctx, namespace, projectID+"-postgres-app", projectID)
	if err != nil {
		return nil, fmt.Errorf("extract credentials: %w", err)
	}

	// Stage 6: Backup
	if req.Backup != nil && req.Backup.Enabled {
		cb(domain.StageBackupConfiguration)
		if err := p.provisionScheduledBackup(ctx, req, projectID, namespace); err != nil {
			return nil, err
		}
	}

	cb(domain.StageMetricsSetup)
	// Stage 8: Watcher deployment deferred to service layer (after cdc_watcher role).
	cb(domain.StageWatcherDeployment)
	cb(domain.StageCompleted)

	creds.Namespace = namespace
	return creds, nil
}

// ProvisionWithRollback mirrors Provision but uses ProvisionContext for stage/step
// tracking and registers compensation actions so the service layer can roll back
// on failure. Namespace delete is the single atomic cleanup that covers stages 2-8
// (CNPG cluster CRD, pods, PVCs, backup secret, scheduled backup, helm watcher are
// all inside the namespace).
func (p *PostgreSQLProvisioner) ProvisionWithRollback(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, pc *ProvisionContext) (*ProvisioningResult, error) {
	projectID := req.ProjectName
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)

	// Stage 1: Validate
	pc.SetStage(domain.StageValidating)

	// Stage 2: Namespace + backup secret
	if err := p.stageNamespace(ctx, req, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 3: CRD (inside namespace — covered by namespace cleanup)
	if err := p.stageCRD(ctx, req, tier, projectID, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 4: Wait for pods
	if err := p.stageWaitForPods(ctx, tier, projectID, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 5: Credentials
	pc.SetStage(domain.StageCredentialGeneration)
	pc.SetStep("read postgres-app secret")
	secretName := projectID + "-postgres-app"
	creds, err := p.extractCredentials(ctx, namespace, secretName, projectID)
	if err != nil {
		return nil, pc.Fail(fmt.Errorf("extract credentials: %w", err))
	}

	// Stage 6: Backup
	if req.Backup != nil && req.Backup.Enabled {
		if err := p.stageBackup(ctx, req, projectID, namespace, pc); err != nil {
			return nil, err
		}
	}

	// Stage 7: Metrics (no-op)
	pc.SetStage(domain.StageMetricsSetup)

	// Stage 8: Watcher deployment is deferred to the service layer (see
	// PostgreSQLProvisioner.DeployWatcher). The cdc_watcher role does not
	// exist until createProjectRoles runs in finalizeProvisioning, so
	// deploying here races the role SQL and crashloops the watcher.
	pc.SetStage(domain.StageWatcherDeployment)

	// Stage 9: Done
	pc.SetStage(domain.StageCompleted)
	creds.Namespace = namespace
	return creds, nil
}

func (p *PostgreSQLProvisioner) stageNamespace(ctx context.Context, req domain.ProvisioningRequest, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageNamespaceCreation)
	pc.SetStep("create namespace")
	labels := map[string]string{
		"excalibase.io/type": "project",
		"excalibase.io/org":  req.OrgID,
	}
	if err := p.client.CreateNamespaceWithLabels(ctx, namespace, labels); err != nil {
		return pc.Fail(fmt.Errorf("create namespace: %w", err))
	}
	pc.RegisterCleanup("delete namespace "+namespace, func(ctx context.Context) error {
		return p.client.DeleteNamespace(ctx, namespace)
	})
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 != nil {
		pc.SetStep("create backup secret")
		if err := p.client.CreateSecret(ctx, namespace, "backup-s3-creds", map[string][]byte{
			"ACCESS_KEY_ID":     []byte(req.Backup.S3.AccessKeyID),
			"ACCESS_SECRET_KEY": []byte(req.Backup.S3.SecretAccessKey),
		}); err != nil {
			return pc.Fail(fmt.Errorf("create backup secret: %w", err))
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageCRD(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageCRDDeployment)
	pc.SetStep("apply CNPG cluster")
	opts := k8s.PostgreSQLClusterOpts{
		ProjectID:       projectID,
		Namespace:       namespace,
		Tier:            tier,
		StorageClass:    req.StorageClass,
		PostgresVersion: req.PostgresVersion,
		DatabaseName:    req.DatabaseName,
		MasterUsername:  req.MasterUsername,
		Parameters:      req.Parameters,
		Tags:            req.Tags,
	}
	if req.Backup != nil && req.Backup.Enabled {
		opts.Backup = &k8s.BackupOpts{
			Schedule:      req.Backup.Schedule,
			RetentionDays: req.Backup.Retention,
		}
		if req.Backup.S3 != nil {
			opts.Backup.EndpointURL = req.Backup.S3.Endpoint
			opts.Backup.Bucket = req.Backup.S3.Bucket
		}
	}
	cluster := k8s.BuildPostgreSQLCluster(opts)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return pc.Fail(fmt.Errorf("deploy cluster CRD: %w", err))
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageWaitForPods(ctx context.Context, tier config.TierConfig, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageWaitingForReady)
	pc.SetStep("primary pod")
	primaryPod := projectID + primaryPodSuffix
	if err := p.waitForPodReady(ctx, namespace, primaryPod, 5*time.Minute); err != nil {
		return pc.Fail(fmt.Errorf("primary pod: %w", err))
	}
	for i := 2; i <= tier.Instances; i++ {
		pc.SetStep(fmt.Sprintf("replica %d", i))
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		if err := p.waitForPodReady(ctx, namespace, pod, 3*time.Minute); err != nil {
			return pc.Fail(fmt.Errorf("replica %d: %w", i, err))
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageBackup(ctx context.Context, req domain.ProvisioningRequest, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageBackupConfiguration)
	pc.SetStep("apply scheduled backup")
	schedule := req.Backup.Schedule
	if schedule == "" {
		schedule = "0 0 * * *"
	}
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup); err != nil {
		return pc.Fail(fmt.Errorf("configure backup: %w", err))
	}
	return nil
}

// Pause patches the project's CNPG cluster spec.instances to 0.
// CNPG operator gracefully drains then scales down all replicas.
// PVCs are kept; data persists. Resume restores the count from
// inst.Tier (caller must pass tier.Instances via the resumeReplicas
// helper since the spec doesn't know the tier).
func (p *PostgreSQLProvisioner) Pause(ctx context.Context, namespace, projectID string) error {
	return p.patchClusterInstances(ctx, namespace, projectID, 0)
}

// Resume patches spec.instances back. K8s provisioner can't infer
// tier.Instances from the cluster CRD itself (it's the *desired*
// count, not embedded in tier metadata), so we read tier from the
// cluster's annotation that the provisioner stamps at create time.
// If the annotation is missing (legacy cluster), fall back to 1.
func (p *PostgreSQLProvisioner) Resume(ctx context.Context, namespace, projectID string) error {
	clusterName := projectID + clusterNameSuffix
	cluster, err := p.client.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster for resume: %w", err)
	}
	count := 1
	if anns, ok, _ := unstructured.NestedStringMap(cluster.Object, "metadata", "annotations"); ok {
		if v, ok := anns["excalibase.io/tier-instances"]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				count = n
			}
		}
	}
	return p.patchClusterInstances(ctx, namespace, projectID, count)
}

func (p *PostgreSQLProvisioner) patchClusterInstances(ctx context.Context, namespace, projectID string, instances int) error {
	clusterName := projectID + clusterNameSuffix
	cluster, err := p.client.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster for instances patch: %w", err)
	}
	if err := unstructured.SetNestedField(cluster.Object, int64(instances), "spec", "instances"); err != nil {
		return fmt.Errorf("set spec.instances: %w", err)
	}
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("apply cluster instances=%d: %w", instances, err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	// Uninstall watcher first (stops replication cleanly)
	if err := p.client.UninstallHelmChart(ctx, namespace, "excalibase-watcher"); err != nil {
		log.Printf("WARN: failed to uninstall watcher: %v", err)
	}
	// Delete the CNPG cluster CRD
	if err := p.client.DeleteCRD(ctx, k8s.CNPGClusterGVR, namespace, projectID+clusterNameSuffix); err != nil {
		log.Printf("WARN: failed to delete cluster CRD: %v", err)
	}
	// Delete namespace (cascades everything)
	return p.client.DeleteNamespace(ctx, namespace)
}

func (p *PostgreSQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	ready, err := p.client.IsPodReady(ctx, namespace, projectID+primaryPodSuffix)
	if err != nil {
		return &ProvisioningStatus{Phase: "Unknown", Message: err.Error()}, nil
	}
	if ready {
		return &ProvisioningStatus{Phase: "Running", Ready: true}, nil
	}
	return &ProvisioningStatus{Phase: "Pending", Ready: false}, nil
}

func (p *PostgreSQLProvisioner) ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error {
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	return p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup)
}

// DeployWatcher installs the per-project CDC watcher Helm chart with inline
// cdc_watcher credentials. Must be called AFTER the role exists (after
// createProjectRoles). Soft-fails: logs WARN if the chart install errors so
// provisioning still completes.
func (p *PostgreSQLProvisioner) DeployWatcher(ctx context.Context, namespace, projectID, dbName, username, password string) error {
	if p.watcherChartPath == "" {
		return nil
	}
	values := map[string]interface{}{
		"postgres": map[string]interface{}{
			"enabled":           true,
			"url":               fmt.Sprintf("postgres://%s-postgres-rw.%s.svc.cluster.local:5432/%s?replication=database", projectID, namespace, dbName),
			"username":          username,
			"password":          password,
			"slotName":          "cdc_watcher",
			"publicationName":   "cdc_watcher_pub",
			"createSlot":        true,
			"createPublication": false,
			"captureDdl":        false,
		},
		"nats": map[string]interface{}{
			"url":           "nats://nats.excalibase-platform.svc.cluster.local:4222",
			"streamName":    "CDC",
			"subjectPrefix": fmt.Sprintf("cdc.%s", projectID),
			"enabled":       true,
		},
		"resources": map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": "200m", "memory": "256Mi"},
			"requests": map[string]interface{}{"cpu": "50m", "memory": "128Mi"},
		},
		"image": map[string]interface{}{
			"repository": "excalibase/excalibase-watcher-go",
			"tag":        "latest",
			"pullPolicy": "IfNotPresent",
		},
	}
	if err := p.client.InstallHelmChart(ctx, namespace, "excalibase-watcher", p.watcherChartPath, values); err != nil {
		log.Printf("WARN: watcher deployment failed for %s: %v", projectID, err)
		return err
	}
	return nil
}

func (p *PostgreSQLProvisioner) waitForPodReady(ctx context.Context, namespace, pod string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := p.client.IsPodReady(ctx, namespace, pod)
		if err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("pod %s did not become ready within %v", pod, timeout)
}

func (p *PostgreSQLProvisioner) extractCredentials(ctx context.Context, namespace, secretName, projectID string) (*ProvisioningResult, error) {
	// Poll for secret with retries, respecting context cancellation.
	var secretData map[string][]byte
	var err error
	for i := 0; i < 30; i++ {
		secretData, err = p.client.GetSecret(ctx, namespace, secretName)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("secret %s not found (context cancelled): %w", secretName, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("secret %s not found: %w", secretName, err)
	}

	dbName := string(secretData["dbname"])
	if dbName == "" {
		dbName = "app"
	}

	return &ProvisioningResult{
		Host:         fmt.Sprintf("%s-postgres-rw.%s.svc.cluster.local", projectID, namespace),
		ReadOnlyHost: fmt.Sprintf("%s-postgres-r.%s.svc.cluster.local", projectID, namespace),
		Port:         5432,
		DatabaseName: dbName,
		Username:     string(secretData["username"]),
		Password:     string(secretData["password"]),
		SSLMode:      "require",
	}, nil
}
