package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// OperationRestore holds a project's lifecycle lease while its database is
// replaced from its own backups.
const OperationRestore ProjectOperation = "restore"

var (
	// ErrInPlaceRestoreUnsupported refuses an in-place restore on a
	// deployment whose backups cannot rebuild a project's database.
	ErrInPlaceRestoreUnsupported = errors.New("replacing a project's database from its backups needs a Kubernetes deployment; restore into a new project instead")
	// ErrInPlaceNoBackups refuses an in-place restore of a project with
	// backups off: there is nothing to restore from.
	ErrInPlaceNoBackups = errors.New("this project has no backups to restore from")
	// ErrInPlaceRestoreNotConfigured is a platform wired without in-place restores.
	ErrInPlaceRestoreNotConfigured = errors.New("restore: in-place restore is not configured")
)

// InPlaceRestoreError is how an in-place restore that did not complete ends:
// Public is written for the user, Cause stays in the log.
type InPlaceRestoreError struct {
	Public string
	Cause  error
}

func (e *InPlaceRestoreError) Error() string {
	if e.Cause == nil {
		return e.Public
	}
	return e.Public + ": " + e.Cause.Error()
}

func (e *InPlaceRestoreError) Unwrap() error { return e.Cause }

// InPlaceCluster is the deployment's half of an in-place restore.
type InPlaceCluster interface {
	// InPlaceRecoveryTarget resolves req against the project's own backups
	// and refuses a point they cannot reach, before anything changes.
	InPlaceRecoveryTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error)
	// TakeSafetyBackup backs the running database up and returns the
	// backup once it has completed.
	TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error)
	// ReplaceDatabase swaps the project's database for one recovered to
	// target under the same name, certificates and credentials, returning
	// once it is up.
	ReplaceDatabase(ctx context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error
}

// InPlaceProjects is what an in-place restore needs from the project.
type InPlaceProjects interface {
	// HoldForInPlaceRestore takes the project's lifecycle lease and admits
	// a project that is running, or one an earlier restore left stopped.
	HoldForInPlaceRestore(ctx context.Context, projectID string) (*domain.DatabaseInstance, func(), error)
	StopReplication(ctx context.Context, inst *domain.DatabaseInstance) error
	RestartReplication(ctx context.Context, inst *domain.DatabaseInstance) error
	// ReapplyCredentials sets the recovered roles' passwords back to the
	// ones on file: the backup carries the passwords of its own time.
	ReapplyCredentials(ctx context.Context, inst *domain.DatabaseInstance) error
	// AnnounceDatabaseReplaced tells the data plane to drop what it cached
	// about the project's tables: they are now the backup's.
	AnnounceDatabaseReplaced(ctx context.Context, projectID string)
}

// InPlaceRestore replaces a project's database with its own backups at a
// chosen point. The project keeps its id, slot, endpoints and credentials,
// so a plan at its project limit can restore. The running database is backed
// up first; a replace that cannot be proved is rolled back to that backup.
type InPlaceRestore struct {
	projects  InPlaceProjects
	instances storage.InstanceStore
	probe     DatabaseProbe
	observers []StatusObserver
}

// InPlaceRestoreConfig wires an InPlaceRestore. Every field is required
// except Observers.
type InPlaceRestoreConfig struct {
	Projects  InPlaceProjects
	Instances storage.InstanceStore
	Probe     DatabaseProbe
	// Observers are told each status the project is given, so pools holding
	// connections to the old database drop them.
	Observers []StatusObserver
}

func NewInPlaceRestore(c InPlaceRestoreConfig) *InPlaceRestore {
	return &InPlaceRestore{projects: c.Projects, instances: c.Instances, probe: c.Probe, observers: c.Observers}
}

// Restore replaces projectID's database with cluster, reporting its phases.
func (r *InPlaceRestore) Restore(ctx context.Context, cluster InPlaceCluster, projectID string, req domain.RestoreRequest) error {
	inst, release, err := r.projects.HoldForInPlaceRestore(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()
	if !backupsOn(inst) {
		return ErrInPlaceNoBackups
	}
	target, err := cluster.InPlaceRecoveryTarget(ctx, inst, req)
	if err != nil {
		return err
	}
	rollback, err := r.safetyPoint(ctx, cluster, inst)
	if err != nil {
		return err
	}

	ReportRestoreProgress(ctx, domain.RestoreStepReplacing)
	if err := r.setStatus(inst.ProjectID, domain.StatusRestoring, domain.RestoreStepReplacing, ""); err != nil {
		return err
	}
	cause := r.replace(ctx, cluster, inst, target)
	if cause == nil {
		return r.setStatus(inst.ProjectID, domain.StatusActive, "", "")
	}
	log.Printf("in-place restore %s: %v", projectID, cause)
	return r.rollBack(ctx, cluster, inst, rollback, cause)
}

// safetyPoint is the backup a failed replace is rolled back to. A project
// left stopped by an earlier restore has no running database to back up;
// it has none, and a failure leaves it stopped as it was.
func (r *InPlaceRestore) safetyPoint(ctx context.Context, cluster InPlaceCluster, inst *domain.DatabaseInstance) (*safetyBackup, error) {
	if inst.Status != string(domain.StatusActive) {
		return nil, nil
	}
	ReportRestoreProgress(ctx, domain.RestoreStepSafetyBackup)
	id, err := cluster.TakeSafetyBackup(ctx, inst)
	if err != nil {
		return nil, &InPlaceRestoreError{
			Public: "the backup taken before replacing the database did not complete, so nothing was changed; try again",
			Cause:  err,
		}
	}
	target, err := cluster.InPlaceRecoveryTarget(ctx, inst, domain.RestoreRequest{BackupID: id})
	if err != nil {
		return nil, &InPlaceRestoreError{
			Public: "the backup taken before replacing the database cannot be restored, so nothing was changed; try again",
			Cause:  err,
		}
	}
	return &safetyBackup{id: id, target: target}, nil
}

type safetyBackup struct {
	id     string
	target map[string]interface{}
}

// replace swaps the database and proves the result: credentials back as
// filed, a query answered with them, and replication running again.
func (r *InPlaceRestore) replace(ctx context.Context, cluster InPlaceCluster, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	// Its replication session would hold the old database's shutdown open,
	// and the slot it reads does not survive a recovery anyway.
	if err := r.projects.StopReplication(ctx, inst); err != nil {
		log.Printf("in-place restore %s: stop replication: %v", inst.ProjectID, err)
	}
	if err := cluster.ReplaceDatabase(ctx, inst, target); err != nil {
		return fmt.Errorf("replace database: %w", err)
	}
	ReportRestoreProgress(ctx, domain.RestoreStepChecking)
	if err := r.projects.ReapplyCredentials(ctx, inst); err != nil {
		return fmt.Errorf("reapply credentials: %w", err)
	}
	if err := r.probe.Probe(ctx, inst.ProjectID); err != nil {
		return fmt.Errorf("probe replaced database: %w", err)
	}
	if err := r.projects.RestartReplication(ctx, inst); err != nil {
		return fmt.Errorf("restart replication: %w", err)
	}
	r.projects.AnnounceDatabaseReplaced(ctx, inst.ProjectID)
	return nil
}

// rollBack puts the safety backup back after a replace that failed. Without
// one, or when that fails too, the project stays stopped and says how to get
// its data back.
func (r *InPlaceRestore) rollBack(ctx context.Context, cluster InPlaceCluster, inst *domain.DatabaseInstance, safety *safetyBackup, cause error) error {
	if safety == nil {
		reason := "the restore did not complete; restore again from Backups"
		if err := r.setStatus(inst.ProjectID, domain.StatusRestoring, domain.RestoreStepRestoreStopped, reason); err != nil {
			log.Printf("in-place restore %s: record failure: %v", inst.ProjectID, err)
		}
		return &InPlaceRestoreError{Public: reason, Cause: cause}
	}
	ReportRestoreProgress(ctx, domain.RestoreStepRollingBack)
	if err := r.replace(ctx, cluster, inst, safety.target); err != nil {
		reason := fmt.Sprintf("the restore did not complete and the database could not be put back automatically; "+
			"its data from just before the restore is in backup %s — restore that backup into this project", safety.id)
		if statusErr := r.setStatus(inst.ProjectID, domain.StatusRestoring, domain.RestoreStepRestoreStopped, reason); statusErr != nil {
			log.Printf("in-place restore %s: record failure: %v", inst.ProjectID, statusErr)
		}
		return &InPlaceRestoreError{Public: reason, Cause: errors.Join(cause, err)}
	}
	if err := r.setStatus(inst.ProjectID, domain.StatusActive, "", ""); err != nil {
		return err
	}
	return &InPlaceRestoreError{
		Public: fmt.Sprintf("the restore did not complete, so the project was put back as it was just before it (backup %s)", safety.id),
		Cause:  cause,
	}
}

// setStatus writes the project's status from its current row, leaving its
// deletion protection and everything else as they are, and tells the
// observers.
func (r *InPlaceRestore) setStatus(projectID string, status domain.ProvisioningStage, step, reason string) error {
	row, err := r.instances.FindByProjectID(projectID)
	if err != nil || row == nil {
		return fmt.Errorf("read project %s: %w", projectID, errors.Join(err, storage.ErrProjectNotFound))
	}
	row.Status = string(status)
	row.CurrentStep = step
	row.FailureReason = reason
	if err := r.instances.Update(row); err != nil {
		return fmt.Errorf("record project %s %s: %w", projectID, status, err)
	}
	for _, o := range r.observers {
		o.ProjectStatusChanged(projectID, string(status))
	}
	return nil
}
