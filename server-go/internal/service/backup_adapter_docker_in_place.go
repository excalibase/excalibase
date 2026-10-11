package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A single host's Postgres project restored in place (EXC-593): its own
// container is rebuilt from its backups under the same name and host port.

var _ InPlaceCluster = (*DockerBackupAdapter)(nil)

// inPlaceRequestKey holds the request, pinned to its base backup, in the
// target an in-place restore carries from resolution to replacement.
const inPlaceRequestKey = "request"

// errInPlaceTargetMalformed is a target this adapter did not resolve.
var errInPlaceTargetMalformed = errors.New("the in-place restore target carries no request")

// InPlaceRecoveryTarget resolves req against the project's own backups before
// anything changes. See InPlaceCluster.
func (a *DockerBackupAdapter) InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	if inst.DocumentDB {
		return nil, fmt.Errorf("%w: a single host's DocumentDB project is restored into a new project", ErrInPlaceRestoreUnsupported)
	}
	base, err := a.resolveSourceBackup(ctx, inst.ProjectID, req)
	if err != nil {
		return nil, err
	}
	pinned := req
	pinned.BackupID = base.ID
	return map[string]interface{}{inPlaceRequestKey: pinned}, nil
}

// TakeSafetyBackup backs the running database up and names the backup.
func (a *DockerBackupAdapter) TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	ref, err := a.TriggerManual(ctx, inst)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

// ReplaceDatabase rebuilds the project's container from target and records
// it on the project. See InPlaceCluster.
func (a *DockerBackupAdapter) ReplaceDatabase(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	req, ok := target[inPlaceRequestKey].(domain.RestoreRequest)
	if !ok {
		return errInPlaceTargetMalformed
	}
	a.mu.RLock()
	dc, plans := a.docker, a.plans
	a.mu.RUnlock()
	if dc == nil {
		return fmt.Errorf("docker restore: docker client not configured (call SetDockerClient)")
	}
	_, limits, err := restorePlanOf(ctx, plans, inst)
	if err != nil {
		return err
	}
	hostPort, err := a.heldPort(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if err := a.retireContainer(ctx, dc, inst); err != nil {
		return err
	}
	seeded, err := a.recoverContainer(ctx, dc, inst, req, recoveryTarget{
		projectID:     inst.ProjectID,
		containerName: provisioner.DatabaseContainerName(inst.ProjectID),
		limits:        limits,
		hostPort:      hostPort,
	})
	if err != nil {
		return err
	}
	if err := a.bringUp(ctx, dc, seeded, backupsOn(inst)); err != nil {
		return err
	}
	return a.recordContainer(inst, seeded.containerID)
}

// retireContainer ships what the running database archived since the safety
// backup, then removes its container and data.
func (a *DockerBackupAdapter) retireContainer(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) error {
	state, err := dc.ContainerState(ctx, inst.Namespace)
	if err != nil {
		return fmt.Errorf("inspect the project's database: %w", err)
	}
	if state.Running && backupsOn(inst) {
		if err := a.RefreshWALArchive(ctx, inst); err != nil {
			return fmt.Errorf("ship the database's last WAL before replacing it: %w", err)
		}
	}
	for _, container := range []string{inst.Namespace, provisioner.DatabaseContainerName(inst.ProjectID)} {
		if err := dc.RemoveContainer(ctx, container); err != nil {
			return fmt.Errorf("remove the project's database: %w", err)
		}
	}
	return nil
}

// recordContainer points the project at its rebuilt container.
func (a *DockerBackupAdapter) recordContainer(inst *domain.DatabaseInstance, containerID string) error {
	a.mu.RLock()
	store := a.instances
	a.mu.RUnlock()
	if store == nil {
		return errors.New("docker restore: instance store not configured")
	}
	row, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || row == nil {
		return fmt.Errorf("read project %s: %w", inst.ProjectID, errors.Join(err, ErrProjectNotFound))
	}
	row.Namespace = containerID
	if err := store.Update(row); err != nil {
		return fmt.Errorf("record the rebuilt database of %s: %w", inst.ProjectID, err)
	}
	inst.Namespace = containerID
	return nil
}

var _ RestoreTargetChecker = (*DockerBackupAdapter)(nil)

// CheckRestoreTarget refuses a point no backup precedes, or one that has not
// happened yet. See RestoreTargetChecker.
func (a *DockerBackupAdapter) CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	if req.TargetTime == nil {
		return nil
	}
	if req.TargetTime.After(time.Now()) {
		return &RestoreTargetError{Message: "the restore point is in the future"}
	}
	_, err := a.resolveSourceBackup(ctx, inst.ProjectID, req)
	return err
}
