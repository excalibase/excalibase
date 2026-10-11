package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// Restore builds a new project from the source's runs at the requested point,
// on its organisation's plan, and registers it once a query answers.
func (a *HostMySQLBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	switch {
	case !a.BackupsConfigured():
		return nil, ErrBackupStorageNotConfigured
	case a.registrar == nil:
		return nil, ErrProjectRegistrarNotConfigured
	case a.probe == nil:
		return nil, ErrDatabaseProbeNotConfigured
	}
	if err := assertProjectIDAvailable(a.instances, req.TargetProjectID); err != nil {
		return nil, err
	}
	restore, err := a.planRestore(ctx, inst, req)
	if err != nil {
		return nil, err
	}
	if backup := restore.plan.Backup; backup != nil && backup.Enabled {
		if backup.Schedule, err = config.MySQLBackupSchedule(restore.plan.Tier); err != nil {
			return nil, err
		}
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	restored, err := a.restoreInto(ctx, pc, inst, req, restore)
	if err != nil {
		return nil, failRestore(ctx, pc, req.TargetProjectID, err)
	}
	return &domain.ProvisioningResponse{
		ProjectID: restored.ProjectID, ProjectName: restored.ProjectName, Status: restored.Status,
		CurrentStage: restored.CurrentStage, Namespace: restored.Namespace, Host: restored.Host,
		Port: restored.Port, DatabaseName: restored.DatabaseName, CreatedAt: restored.CreatedAt,
	}, nil
}

func (a *HostMySQLBackupAdapter) restoreInto(ctx context.Context, pc *provisioner.ProvisionContext, inst *domain.DatabaseInstance,
	req domain.RestoreRequest, restore plannedRestore) (*domain.DatabaseInstance, error) {
	unlock := a.lock(req.TargetProjectID)
	defer unlock()
	container, err := a.build(ctx, pc, req.TargetProjectID, restore)
	if err != nil {
		return nil, err
	}
	restored := hostMySQLRestoredRow(inst, req, container, restore.plan)
	if err := registerVerifiedProject(ctx, pc, a.registrar, a.instances, a.probe, restored, RegistrationOptions{}); err != nil {
		return nil, err
	}
	return restored, nil
}

// hostMySQLRestoredRow is the copy's project row: the source's org, owner,
// database and version, on the plan it was sized by.
func hostMySQLRestoredRow(src *domain.DatabaseInstance, req domain.RestoreRequest, container string, plan RestorePlan) *domain.DatabaseInstance {
	database := src.DatabaseName
	if database == "" {
		database = defaultRestoreDatabase
	}
	port := 3306
	restored := &domain.DatabaseInstance{
		ProjectID: req.TargetProjectID, ProjectName: req.NewProjectName, OrgID: src.OrgID, OwnerID: src.OwnerID,
		DBType: domain.MySQL, Tier: plan.Tier, DeploymentMode: domain.ModeDocker, Namespace: container, Host: container,
		Port: &port, DatabaseName: database, Username: provisioner.MySQLUserAdmin, SSLMode: "REQUIRED",
		MySQLVersion: src.MySQLVersion, StorageClass: src.StorageClass, StorageSize: plan.Config.StorageSize,
		RestoredFromProjectID: src.ProjectID, RestoredFromBackupID: req.BackupID,
		CreatedAt: &domain.FlexTime{Time: time.Now()},
	}
	if backup := plan.Backup; backup != nil {
		restored.BackupEnabled = boolPtr(backup.Enabled)
		restored.BackupSchedule = backup.Schedule
		restored.BackupRetentionDays = intPtr(backup.Retention)
	}
	return restored
}

// The in-place target names the runs by id and server, so ReplaceDatabase
// finds them again after the safety backup added one.
const (
	hostTargetBase   = "base"
	hostTargetCover  = "cover"
	hostTargetPoint  = "point"
	hostTargetReplay = "replay"
)

// InPlaceRecoveryTarget resolves req against the project's own runs.
func (a *HostMySQLBackupAdapter) InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return nil, err
	}
	point, err := resolveHostMySQLPoint(runs, req)
	if err != nil {
		return nil, err
	}
	if _, err := a.planLoad(ctx, inst.ProjectID, point); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		hostTargetBase:   hostMySQLRunDirectory(point.Base.ID, point.Base.Server),
		hostTargetCover:  hostMySQLRunDirectory(point.Cover.ID, point.Cover.Server),
		hostTargetPoint:  point.Point.UTC().Format(time.RFC3339),
		hostTargetReplay: point.Replay,
	}, nil
}

// TakeSafetyBackup backs the running database up and names the run.
func (a *HostMySQLBackupAdapter) TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	ref, err := a.TriggerManual(ctx, inst)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

// ReplaceDatabase removes the project's container, its data with it, and
// builds it again under the same name from target. The safety backup taken
// before is what a failure is rolled back to.
func (a *HostMySQLBackupAdapter) ReplaceDatabase(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	point, err := a.pointOf(ctx, inst, target)
	if err != nil {
		return err
	}
	restore, err := a.planRestoreAt(ctx, inst, point)
	if err != nil {
		return err
	}
	unlock := a.lock(inst.ProjectID)
	defer unlock()
	if err := a.mysql.RemoveContainer(ctx, inst.Namespace); err != nil {
		return fmt.Errorf("remove the project's container: %w", err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	if _, err := a.build(ctx, pc, inst.ProjectID, restore); err != nil {
		return err
	}
	return nil
}

// pointOf finds the runs a target names.
func (a *HostMySQLBackupAdapter) pointOf(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) (hostMySQLPoint, error) {
	base, _ := target[hostTargetBase].(string)
	cover, _ := target[hostTargetCover].(string)
	raw, _ := target[hostTargetPoint].(string)
	replay, _ := target[hostTargetReplay].(bool)
	when, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return hostMySQLPoint{}, fmt.Errorf("in-place restore %s: unreadable restore point %q", inst.ProjectID, raw)
	}
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return hostMySQLPoint{}, err
	}
	point := hostMySQLPoint{Point: when, Replay: replay}
	for _, run := range completedHostMySQLRuns(runs) {
		directory := hostMySQLRunDirectory(run.ID, run.Server)
		if directory == base {
			point.Base = run
		}
		if directory == cover {
			point.Cover = run
		}
	}
	if point.Base.ID == "" || point.Cover.ID == "" {
		return hostMySQLPoint{}, fmt.Errorf("in-place restore %s: backup %s is no longer in the bucket", inst.ProjectID, base)
	}
	return point, nil
}

// planRestoreAt is planRestore for a point already resolved.
func (a *HostMySQLBackupAdapter) planRestoreAt(ctx context.Context, inst *domain.DatabaseInstance, point hostMySQLPoint) (plannedRestore, error) {
	if a.plans == nil {
		return plannedRestore{}, ErrRestorePlanNotConfigured
	}
	image, err := hostMySQLImage(inst)
	if err != nil {
		return plannedRestore{}, err
	}
	load, err := a.planLoad(ctx, inst.ProjectID, point)
	if err != nil {
		return plannedRestore{}, err
	}
	plan, err := a.plans.RestorePlan(ctx, inst)
	if err != nil {
		return plannedRestore{}, fmt.Errorf("size the rebuilt container of %s: %w", inst.ProjectID, err)
	}
	return plannedRestore{load: load, image: image, plan: plan}, nil
}
