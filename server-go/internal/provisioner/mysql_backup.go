package provisioner

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// MySQL backups (ADR 0041 decision 6): MOCO runs the project's BackupPolicy
// as a CronJob writing to the project's own prefix, with a temporary
// credential confined to it.

// SetBackupPathStyle says whether the store is addressed path-style
// (BACKUP_S3_PATH_STYLE), as the platform itself addresses it.
func (p *MySQLProvisioner) SetBackupPathStyle(pathStyle bool) { p.backupPathStyle = pathStyle }

// BackupStore is where the jobs find the bucket the credential works against.
func (p *MySQLProvisioner) BackupStore(creds *domain.S3Credentials) k8s.MySQLBackupStore {
	return k8s.MySQLBackupStore{Bucket: creds.Bucket, Endpoint: creds.Endpoint, Region: creds.Region, PathStyle: p.backupPathStyle}
}

// validateMySQLBackup refuses enabled backups with no schedule MOCO can run
// or no credential to run them with.
func validateMySQLBackup(backup *domain.BackupSettings) error {
	if backup == nil || !backup.Enabled {
		return nil
	}
	if backup.S3 == nil {
		return errors.New("MySQL backups need the project's object-store credentials")
	}
	return k8s.ValidateBackupSchedule(backup.Schedule)
}

// WriteBackupAccess puts a temporary credential into the namespace as the
// named Secret and makes sure the account the jobs run as exists.
func (p *MySQLProvisioner) WriteBackupAccess(ctx context.Context, namespace, secret string, creds *domain.S3Credentials) error {
	data, err := k8s.MySQLBackupCredentialsSecretData(creds)
	if err != nil {
		return err
	}
	if _, err := p.client.GetSecret(ctx, namespace, secret); err == nil {
		err = p.client.UpdateSecret(ctx, namespace, secret, data)
		if err != nil {
			return fmt.Errorf("update %s: %w", secret, err)
		}
	} else if err := p.client.CreateSecret(ctx, namespace, secret, data); err != nil {
		return fmt.Errorf("create %s: %w", secret, err)
	}
	if err := p.client.ApplyCRD(ctx, k8s.ServiceAccountGVR, namespace, k8s.BuildMySQLBackupServiceAccount(namespace)); err != nil {
		return fmt.Errorf("create the backup service account: %w", err)
	}
	return nil
}

// AttachBackups applies the project's BackupPolicy and links the cluster to
// it; MOCO then runs the CronJob.
func (p *MySQLProvisioner) AttachBackups(ctx context.Context, opts k8s.MySQLBackupOpts) error {
	policy, err := k8s.BuildMySQLBackupPolicy(opts)
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.MOCOBackupPolicyGVR, opts.Namespace, policy); err != nil {
		return fmt.Errorf("apply the backup policy: %w", err)
	}
	return p.updateCluster(ctx, opts.Namespace, opts.ProjectID, func(cluster *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return k8s.WithMySQLBackupPolicy(cluster, opts.ProjectID), nil
	})
}

// attachProvisionedBackups starts a new project's backups once it serves.
func (p *MySQLProvisioner) attachProvisionedBackups(ctx context.Context, req domain.ProvisioningRequest, opts k8s.MySQLClusterOpts, pc *ProvisionContext) error {
	if req.Backup == nil || !req.Backup.Enabled {
		return nil
	}
	pc.SetStage(domain.StageBackupConfiguration)
	pc.SetStep("apply the backup policy")
	return p.AttachBackups(ctx, k8s.MySQLBackupOpts{
		ProjectID: opts.ProjectID, Namespace: opts.Namespace, Schedule: req.Backup.Schedule,
		Plan: opts.Tier, Disk: opts.Tier.StorageSize, StorageClass: opts.StorageClass, Store: p.BackupStore(req.Backup.S3),
	})
}

// SetBackupPlan moves the project's backups onto a plan's schedule and sizing.
func (p *MySQLProvisioner) SetBackupPlan(ctx context.Context, namespace, projectID, schedule string, plan config.TierConfig) error {
	return p.updatePolicy(ctx, namespace, projectID, func(policy *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return k8s.WithMySQLBackupSchedule(policy, schedule, plan)
	})
}

// ConfigureBackup moves the project's backups onto another schedule; MOCO
// never deletes old backups, so retention is the platform's purge.
func (p *MySQLProvisioner) ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, _ int) error {
	if err := k8s.ValidateBackupSchedule(schedule); err != nil {
		return err
	}
	return p.updatePolicy(ctx, namespace, projectID, func(policy *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		out := policy.DeepCopy()
		return out, unstructured.SetNestedField(out.Object, schedule, "spec", "schedule")
	})
}

func (p *MySQLProvisioner) updatePolicy(ctx context.Context, namespace, projectID string,
	change func(*unstructured.Unstructured) (*unstructured.Unstructured, error)) error {
	policy, err := p.client.GetCRD(ctx, k8s.MOCOBackupPolicyGVR, namespace, k8s.MySQLBackupPolicyName(projectID))
	if err != nil {
		return fmt.Errorf("read the backup policy: %w", err)
	}
	changed, err := change(policy)
	if err != nil {
		return err
	}
	if err := p.client.UpdateCRD(ctx, k8s.MOCOBackupPolicyGVR, namespace, changed); err != nil {
		return fmt.Errorf("update the backup policy: %w", err)
	}
	return nil
}

// ApplyRestoredCluster creates the settings and a cluster MOCO fills from a
// backup; it has no backups of its own until AttachBackups.
func (p *MySQLProvisioner) ApplyRestoredCluster(ctx context.Context, opts k8s.MySQLClusterOpts, source k8s.MySQLRestoreSource, job k8s.MySQLBackupOpts) error {
	settings, err := k8s.BuildMySQLConfig(opts)
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.ConfigMapGVR, opts.Namespace, settings); err != nil {
		return fmt.Errorf("apply mysqld settings: %w", err)
	}
	cluster, err := k8s.BuildMySQLCluster(opts)
	if err != nil {
		return err
	}
	return p.applyRestore(ctx, cluster, source, job)
}

func (p *MySQLProvisioner) applyRestore(ctx context.Context, cluster *unstructured.Unstructured, source k8s.MySQLRestoreSource, job k8s.MySQLBackupOpts) error {
	restoring, err := k8s.WithMySQLRestore(cluster, source, job)
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.MOCOClusterGVR, cluster.GetNamespace(), restoring); err != nil {
		return fmt.Errorf("create the restoring MySQL cluster: %w", err)
	}
	return nil
}

// AwaitRestored returns once MOCO reports the restore done and the cluster
// serves; a failed restore Job ends the wait at once.
func (p *MySQLProvisioner) AwaitRestored(ctx context.Context, namespace, projectID string, instances int) error {
	return p.readyPoller.WaitUntilReady(ctx, "restore of "+k8s.MySQLClusterName(projectID), func(ctx context.Context) (bool, error) {
		if err := p.restoreJobFailure(ctx, namespace, projectID); err != nil {
			return false, err
		}
		cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
		if err != nil || !k8s.MySQLClusterRestored(cluster) {
			return false, nil
		}
		return p.serving(ctx, namespace, projectID, instances)
	})
}

func (p *MySQLProvisioner) restoreJobFailure(ctx context.Context, namespace, projectID string) error {
	job, err := p.client.GetCRD(ctx, k8s.JobGVR, namespace, k8s.MySQLRestoreJobName(projectID))
	if err != nil {
		return nil
	}
	if state := k8s.MySQLBackupJobState(job); state.Phase == k8s.MySQLJobFailed {
		return fmt.Errorf("the restore job failed: %s", state.Reason)
	}
	return nil
}

// removeBackupObjects deletes the policy, credential and job account of a
// database removed from a namespace that stays; its backups stay in the bucket.
func (p *MySQLProvisioner) removeBackupObjects(ctx context.Context, namespace, projectID string) error {
	if err := p.client.DeleteCRD(ctx, k8s.MOCOBackupPolicyGVR, namespace, k8s.MySQLBackupPolicyName(projectID)); err != nil {
		return fmt.Errorf("delete the backup policy: %w", err)
	}
	if err := p.client.DeleteSecret(ctx, namespace, k8s.MySQLBackupCredentialsSecret); err != nil {
		return fmt.Errorf("delete the backup credentials: %w", err)
	}
	if err := p.client.DeleteCRD(ctx, k8s.ServiceAccountGVR, namespace, k8s.MySQLBackupServiceAccount); err != nil {
		return fmt.Errorf("delete the backup service account: %w", err)
	}
	return nil
}
