package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A single host's project restored in place (EXC-593): its own container is
// rebuilt from its backups under the same name and host port. A DocumentDB
// project's gateway is rebuilt too, keeping its port and certificate (EXC-594).

var _ InPlaceCluster = (*DockerBackupAdapter)(nil)

// inPlaceRequestKey holds the request, pinned to its base backup, in the
// target an in-place restore carries from resolution to replacement.
const inPlaceRequestKey = "request"

// errInPlaceTargetMalformed is a target this adapter did not resolve.
var errInPlaceTargetMalformed = errors.New("the in-place restore target carries no request")

// InPlaceRecoveryTarget resolves req against the project's own backups before
// anything changes. See InPlaceCluster.
func (a *DockerBackupAdapter) InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
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
	plan, limits, err := restorePlanOf(ctx, plans, inst)
	if err != nil {
		return err
	}
	ports, err := a.heldPorts(ctx, inst.ProjectID, inst.DocumentDB)
	if err != nil {
		return err
	}
	gatewayTLS, err := keptGatewayTLS(ctx, dc, inst)
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
		ports:         ports,
		command:       provisioner.PostgresCommand(plan.Config),
	})
	if err != nil {
		return err
	}
	if err := a.bringUp(ctx, dc, seeded, backupsOn(inst)); err != nil {
		return err
	}
	if err := rebuildGateway(ctx, dc, inst, seeded, gatewayTLS); err != nil {
		return err
	}
	return a.recordContainer(inst, seeded.containerID)
}

// keptGatewayTLS is what a DocumentDB project's gateway presents now, read
// before anything is replaced; nil when it has no gateway to keep.
func keptGatewayTLS(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) (*provisioner.GatewayTLS, error) {
	if !inst.DocumentDB {
		return nil, nil
	}
	kept, found, err := provisioner.ReadDocumentDBGatewayTLS(ctx, dc, inst.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read the DocumentDB gateway's certificate: %w", err)
	}
	if !found {
		return nil, nil
	}
	return &kept, nil
}

// rebuildGateway gives a rebuilt DocumentDB project its gateway again, with
// the certificate it had, or a new one when it had no gateway.
func rebuildGateway(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance, seeded recovered, kept *provisioner.GatewayTLS) error {
	if !inst.DocumentDB {
		return nil
	}
	limits := seeded.spec.gatewayLimits()
	var err error
	if kept == nil {
		err = provisioner.StartDocumentDBGateway(ctx, dc, inst.ProjectID, seeded.containerID, limits)
	} else {
		err = provisioner.StartDocumentDBGatewayWithTLS(ctx, dc, inst.ProjectID, seeded.containerID, limits, *kept)
	}
	if err != nil {
		return fmt.Errorf("start the rebuilt DocumentDB gateway: %w", err)
	}
	return nil
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
	// Podman refuses to remove a container another joined: the gateway first.
	containers := []string{inst.Namespace, provisioner.DatabaseContainerName(inst.ProjectID)}
	if inst.DocumentDB {
		containers = append([]string{provisioner.DocumentDBGatewayName(inst.ProjectID)}, containers...)
	}
	for _, container := range containers {
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
