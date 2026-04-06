package provisioner

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

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

func (p *PostgreSQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)
	projectID := req.ProjectName

	// Stage 1: Validate
	cb(domain.StageValidating)

	// Stage 2: Create namespace with labels for NetworkPolicy selectors
	cb(domain.StageNamespaceCreation)
	labels := map[string]string{
		"excalibase.io/type": "project",
		"excalibase.io/org":  req.OrgID,
	}
	if err := p.client.CreateNamespaceWithLabels(ctx, namespace, labels); err != nil {
		return nil, fmt.Errorf("create namespace: %w", err)
	}

	// Create S3 backup credentials secret if backup enabled
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 != nil {
		if err := p.client.CreateSecret(ctx, namespace, "backup-s3-creds", map[string][]byte{
			"ACCESS_KEY_ID":     []byte(req.Backup.S3.AccessKeyID),
			"ACCESS_SECRET_KEY": []byte(req.Backup.S3.SecretAccessKey),
		}); err != nil {
			return nil, fmt.Errorf("create backup secret: %w", err)
		}
	}

	// Stage 3: Deploy CRD
	cb(domain.StageCRDDeployment)
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
	}

	cluster := k8s.BuildPostgreSQLCluster(opts)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return nil, fmt.Errorf("deploy cluster CRD: %w", err)
	}

	// Stage 4: Wait for ready
	cb(domain.StageWaitingForReady)
	primaryPod := projectID + "-postgres-1"
	if err := p.waitForPodReady(ctx, namespace, primaryPod, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("waiting for ready: %w", err)
	}
	// Wait for all replicas
	for i := 2; i <= tier.Instances; i++ {
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		if err := p.waitForPodReady(ctx, namespace, pod, 3*time.Minute); err != nil {
			return nil, fmt.Errorf("waiting for replica %d: %w", i, err)
		}
	}

	// Stage 5: Extract credentials
	cb(domain.StageCredentialGeneration)
	secretName := projectID + "-postgres-app"
	creds, err := p.extractCredentials(ctx, namespace, secretName, projectID)
	if err != nil {
		return nil, fmt.Errorf("extract credentials: %w", err)
	}

	// Stage 6: Configure backup
	if req.Backup != nil && req.Backup.Enabled {
		cb(domain.StageBackupConfiguration)
		schedule := req.Backup.Schedule
		if schedule == "" {
			schedule = "0 0 * * *"
		}
		backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
		if err := p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup); err != nil {
			return nil, fmt.Errorf("configure backup: %w", err)
		}
	}

	// Stage 7: Metrics setup
	cb(domain.StageMetricsSetup)

	// Stage 8: Deploy per-project watcher for CDC
	cb(domain.StageWatcherDeployment)
	if p.watcherChartPath != "" {
		watcherValues := map[string]interface{}{
			"postgres": map[string]interface{}{
				"enabled":                      true,
				"url":                          fmt.Sprintf("jdbc:postgresql://%s-postgres-rw.%s.svc.cluster.local:5432/%s", projectID, namespace, creds.DatabaseName),
				"existingSecret":               fmt.Sprintf("%s-postgres-superuser", projectID),
				"existingSecretUsernameKey":     "username",
				"existingSecretPasswordKey":     "password",
				"slotName":                      fmt.Sprintf("cdc_%s", projectID),
				"publicationName":              fmt.Sprintf("cdc_%s_pub", projectID),
				"createSlotIfNotExists":        true,
				"createPublicationIfNotExists": true,
				"captureDdl":                   true,
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
		}
		if err := p.client.InstallHelmChart(ctx, namespace, "excalibase-watcher", p.watcherChartPath, watcherValues); err != nil {
			log.Printf("WARN: watcher deployment failed for %s: %v", projectID, err)
		}
	}

	// Stage 9: Completed
	cb(domain.StageCompleted)

	creds.Namespace = namespace
	return creds, nil
}

func (p *PostgreSQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	// Uninstall watcher first (stops replication cleanly)
	if err := p.client.UninstallHelmChart(ctx, namespace, "excalibase-watcher"); err != nil {
		log.Printf("WARN: failed to uninstall watcher: %v", err)
	}
	// Delete the CNPG cluster CRD
	if err := p.client.DeleteCRD(ctx, k8s.CNPGClusterGVR, namespace, projectID+"-postgres"); err != nil {
		log.Printf("WARN: failed to delete cluster CRD: %v", err)
	}
	// Delete namespace (cascades everything)
	return p.client.DeleteNamespace(ctx, namespace)
}

func (p *PostgreSQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	ready, err := p.client.IsPodReady(ctx, namespace, projectID+"-postgres-1")
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
