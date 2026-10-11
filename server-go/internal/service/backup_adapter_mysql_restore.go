package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// mysqlRestoreTarget is where a MySQL restore lands and what it runs on.
type mysqlRestoreTarget struct {
	project   string
	namespace string
	plan      RestorePlan
	point     time.Time
	image     string
	// store is the platform's; only its location reaches the namespace.
	store       *domain.S3Credentials
	sourceCreds *domain.S3Credentials
	ownCreds    *domain.S3Credentials
}

// requireRestoreWiring refuses a restore the platform could not finish.
func (a *MySQLBackupAdapter) requireRestoreWiring() error {
	switch {
	case !a.BackupsConfigured():
		return ErrBackupStorageNotConfigured
	case a.registrar == nil:
		return ErrProjectRegistrarNotConfigured
	case a.probe == nil:
		return ErrDatabaseProbeNotConfigured
	case a.plans == nil:
		return ErrRestorePlanNotConfigured
	case a.creds == nil:
		return ErrBackupCredentialsNotConfigured
	}
	return nil
}

// Restore builds a new project whose MySQLCluster MOCO fills from the
// source's backups at the requested time, on its organisation's plan.
func (a *MySQLBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	if err := a.requireRestoreWiring(); err != nil {
		return nil, err
	}
	if inst.OrgID == "" {
		return nil, fmt.Errorf("restore source %s: %w", inst.ProjectID, k8s.ErrProjectOrgRequired)
	}
	if err := assertProjectIDAvailable(a.instances, req.TargetProjectID); err != nil {
		return nil, err
	}
	target, err := a.planRestore(ctx, inst, req)
	if err != nil {
		return nil, err
	}
	if err := a.issueRestoreCredentials(ctx, inst, &target); err != nil {
		return nil, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	restored, err := a.runRestore(ctx, pc, inst, req, target)
	if err != nil {
		return nil, failRestore(ctx, pc, target.project, err)
	}
	return &domain.ProvisioningResponse{
		ProjectID: restored.ProjectID, ProjectName: restored.ProjectName, Status: restored.Status,
		CurrentStage: restored.CurrentStage, Namespace: restored.Namespace, Host: restored.Host,
		Port: restored.Port, DatabaseName: restored.DatabaseName, CreatedAt: restored.CreatedAt,
	}, nil
}

// planRestore settles, before anything is created, the point, the image and
// the plan: the organisation's, with the plan's MySQL backup schedule.
func (a *MySQLBackupAdapter) planRestore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (mysqlRestoreTarget, error) {
	image, err := config.MySQLImage(inst.MySQLVersion)
	if err != nil {
		return mysqlRestoreTarget{}, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return mysqlRestoreTarget{}, err
	}
	point, err := resolveMySQLRestorePoint(backups, req)
	if err != nil {
		return mysqlRestoreTarget{}, err
	}
	plan, err := a.plans.RestorePlan(ctx, inst)
	if err != nil {
		return mysqlRestoreTarget{}, fmt.Errorf("restore %s: %w", inst.ProjectID, err)
	}
	if plan.Config, err = config.MySQLPlan(plan.Config); err != nil {
		return mysqlRestoreTarget{}, err
	}
	if backup := plan.Backup; backup != nil && backup.Enabled {
		if backup.Schedule, err = config.MySQLBackupSchedule(plan.Tier); err != nil {
			return mysqlRestoreTarget{}, err
		}
	}
	store, _ := a.storage.BackupStorage()
	return mysqlRestoreTarget{
		project: req.TargetProjectID, namespace: fmt.Sprintf("%s-%s", inst.OrgID, req.TargetProjectID),
		plan: plan, point: point, image: image, store: store,
	}, nil
}

// issueRestoreCredentials mints the source's prefix read-only and, when the
// plan backs the copy up, its own empty prefix read-write.
func (a *MySQLBackupAdapter) issueRestoreCredentials(ctx context.Context, inst *domain.DatabaseInstance, target *mysqlRestoreTarget) error {
	source, err := a.creds.ForRestoreSourcePrefix(ctx, target.store, inst.ProjectID, k8s.MOCOBackupPrefix(inst.Namespace, inst.ProjectID))
	if err != nil {
		return err
	}
	target.sourceCreds = source
	if backup := target.plan.Backup; backup != nil && backup.Enabled {
		own, err := a.creds.ForNewPrefix(ctx, target.store, target.project, k8s.MOCOBackupPrefix(target.namespace, target.project))
		if err != nil {
			return err
		}
		target.ownCreds = own
	}
	return nil
}

// runRestore drives the copy to a project proved usable; every failure is
// returned so Restore compensates through pc.
func (a *MySQLBackupAdapter) runRestore(ctx context.Context, pc *provisioner.ProvisionContext, inst *domain.DatabaseInstance,
	req domain.RestoreRequest, target mysqlRestoreTarget) (*domain.DatabaseInstance, error) {
	if err := a.createRestoredCluster(ctx, pc, inst, target); err != nil {
		return nil, err
	}
	if err := a.mysql.AwaitRestored(ctx, a.poller, target.namespace, target.project, target.plan.Config.Instances); err != nil {
		return nil, err
	}
	if target.ownCreds != nil {
		if err := a.mysql.AttachBackups(ctx, a.backupOpts(inst, target)); err != nil {
			return nil, err
		}
	}
	restored := mysqlRestoredRow(inst, req, target)
	if err := registerVerifiedProject(ctx, pc, a.registrar, a.instances, a.probe, restored, RegistrationOptions{}); err != nil {
		return nil, err
	}
	return restored, nil
}

// createRestoredCluster creates the namespace (whose deletion compensates
// everything inside it), the credentials and the restoring cluster.
func (a *MySQLBackupAdapter) createRestoredCluster(ctx context.Context, pc *provisioner.ProvisionContext, inst *domain.DatabaseInstance, target mysqlRestoreTarget) error {
	if err := a.kube.CreateProjectNamespace(ctx, target.namespace, inst.OrgID); err != nil {
		return fmt.Errorf("create restore namespace: %w", err)
	}
	pc.RegisterCleanup("delete restore namespace", func(ctx context.Context) error {
		return a.kube.DeleteNamespace(ctx, target.namespace)
	})
	if err := a.mysql.WriteBackupAccess(ctx, target.namespace, k8s.MySQLRestoreSourceAccessName, target.sourceCreds); err != nil {
		return err
	}
	if target.ownCreds != nil {
		if err := a.mysql.WriteBackupAccess(ctx, target.namespace, k8s.MySQLBackupAccessName, target.ownCreds); err != nil {
			return err
		}
	}
	opts := k8s.MySQLClusterOpts{
		ProjectID: target.project, Namespace: target.namespace, Tier: target.plan.Config,
		StorageClass: inst.StorageClass, Image: target.image,
	}
	source := k8s.MySQLRestoreSource{
		Namespace: inst.Namespace, ProjectID: inst.ProjectID, Point: target.point,
		CredentialsSecret: k8s.MySQLRestoreSourceAccessName,
	}
	return a.mysql.ApplyRestoredCluster(ctx, opts, source, a.backupOpts(inst, target))
}

// backupOpts is the job shape for the copy: its plan, disk and the store's location.
func (a *MySQLBackupAdapter) backupOpts(inst *domain.DatabaseInstance, target mysqlRestoreTarget) k8s.MySQLBackupOpts {
	opts := k8s.MySQLBackupOpts{
		ProjectID: target.project, Namespace: target.namespace, Plan: target.plan.Config,
		Disk: target.plan.Config.StorageSize, StorageClass: inst.StorageClass, Store: a.mysql.BackupStore(target.store),
	}
	if backup := target.plan.Backup; backup != nil {
		opts.Schedule = backup.Schedule
	}
	return opts
}

// mysqlRestoredRow is the copy's project row: the source's org, owner,
// database and version, on the plan it was sized by.
func mysqlRestoredRow(src *domain.DatabaseInstance, req domain.RestoreRequest, target mysqlRestoreTarget) *domain.DatabaseInstance {
	database := src.DatabaseName
	if database == "" {
		database = defaultRestoreDatabase
	}
	port := 3306
	restored := &domain.DatabaseInstance{
		ProjectID: target.project, ProjectName: req.NewProjectName, OrgID: src.OrgID, OwnerID: src.OwnerID,
		DBType: domain.MySQL, Tier: target.plan.Tier, DeploymentMode: domain.ModeK8s, Namespace: target.namespace,
		Host:         k8s.MySQLPrimaryHost(target.project, target.namespace),
		ReadOnlyHost: k8s.MySQLReplicaHost(target.project, target.namespace),
		Port:         &port, DatabaseName: database, Username: provisioner.MySQLUserAdmin, SSLMode: "REQUIRED",
		MySQLVersion: src.MySQLVersion, StorageClass: src.StorageClass, StorageSize: target.plan.Config.StorageSize,
		RestoredFromProjectID: src.ProjectID, RestoredFromBackupID: req.BackupID,
		CreatedAt: &domain.FlexTime{Time: time.Now()},
	}
	if backup := target.plan.Backup; backup != nil {
		restored.BackupEnabled = boolPtr(backup.Enabled)
		restored.BackupSchedule = backup.Schedule
		restored.BackupRetentionDays = intPtr(backup.Retention)
	}
	return restored
}
