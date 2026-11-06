package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ErrRestoreNotObserved is the single reason a caller is told a restore
// failed. Recovery is an operator concern: which step gave up, which cluster
// phase it saw and which connection was refused all go to the log, never to
// the client.
var ErrRestoreNotObserved = errors.New("restore did not complete: the recovered database was not confirmed usable")

// ErrRestoreTargetDeleting is returned when the target project was claimed
// for deletion while its restore was still running. Deletion owns the
// resources from that moment, so the restore stops without compensating.
var ErrRestoreTargetDeleting = errors.New("restore stopped: the target project is being deleted")

// ErrDatabaseProbeNotConfigured is returned when a restore has no way to
// prove the recovered database answers queries. There is no fallback: a
// restore that cannot be verified must not run.
var ErrDatabaseProbeNotConfigured = errors.New("restore: database probe not configured")

// DatabaseProbe proves a project's database is reachable and serving with
// the credentials the platform filed for it.
type DatabaseProbe interface {
	Probe(ctx context.Context, projectID string) error
}

// unverifiedRowCleanup names the removal of a restored copy's row. Its presence
// in a rollback's results is what says this restore created the row.
const unverifiedRowCleanup = "remove unverified restored project"

// failedRestoreReason is what a copy that could not be fully removed reads.
const failedRestoreReason = "the restore did not complete and what it created is still being removed"

// registerVerifiedProject finishes a restore the honest way: the project is
// registered as RESTORING, given the source's API settings (EXC-639), the
// database is made to answer a query with the credentials that registration
// filed, and only then does the row become ACTIVE. Removing the unverified row
// (and with it the copied settings) is registered on pc, so a failed probe
// rolls back through the same compensation chain as everything else the
// restore created.
func registerVerifiedProject(
	ctx context.Context,
	pc *provisioner.ProvisionContext,
	reg ProjectRegistrar,
	store storage.InstanceStore,
	probe DatabaseProbe,
	inst *domain.DatabaseInstance,
	opts RegistrationOptions,
) error {
	if probe == nil {
		return ErrDatabaseProbeNotConfigured
	}
	opts.Context = pc
	opts.Unverified = true
	if err := reg.RegisterProject(ctx, inst, opts); err != nil {
		return fmt.Errorf("register restored project: %w", err)
	}
	projectID := inst.ProjectID
	// Last, because the row is the only record of the cluster: it goes only
	// once everything it records has gone.
	pc.RegisterLastCleanup(unverifiedRowCleanup, func(context.Context) error {
		return store.Delete(projectID)
	})

	if inst.RestoredFromProjectID == "" {
		return errors.New("restore: the copy does not name its source")
	}
	if err := reg.CarryRestoredSettings(ctx, pc, inst.RestoredFromProjectID, projectID); err != nil {
		return fmt.Errorf("carry the source's settings: %w", err)
	}

	if err := probe.Probe(ctx, projectID); err != nil {
		return fmt.Errorf("probe restored database: %w", err)
	}

	markProjectActive(inst)
	if err := store.Update(inst); err != nil {
		if errors.Is(err, storage.ErrProjectDeleting) {
			return ErrRestoreTargetDeleting
		}
		return fmt.Errorf("activate restored project: %w", err)
	}
	return nil
}

// failRestore turns any internal restore failure into the one reason clients
// are given, after running the compensations for everything the restore
// created. A target claimed by DELETE is the exception: its resources now
// belong to the deletion pipeline, so nothing is compensated. A compensation
// that fails leaves a record on the project row, for the sweep and for DELETE.
func failRestore(ctx context.Context, pc *provisioner.ProvisionContext, store storage.InstanceStore, record *domain.DatabaseInstance, err error) error {
	projectID := record.ProjectID
	if errors.Is(err, ErrRestoreTargetDeleting) {
		log.Printf("restore %s: stopped, target claimed for deletion", projectID)
		return ErrRestoreTargetDeleting
	}
	results := pc.Rollback(ctx)
	log.Printf("restore %s failed: %v; compensations: %+v", projectID, err, results)
	if rollbackLeftSomething(results) {
		keepFailedRestoreOnRecord(store, record, results)
	}
	// Missing the target is the caller's to know: it changes what to ask for.
	if errors.Is(err, ErrRestoreTargetNotReached) {
		return ErrRestoreTargetNotReached
	}
	// So is the plan having no room left for the copy (EXC-639).
	var limit *OrgProjectLimitError
	if errors.As(err, &limit) {
		return limit
	}
	return ErrRestoreNotObserved
}

func rollbackLeftSomething(results []provisioner.CleanupResult) bool {
	for _, result := range results {
		if !result.OK {
			return true
		}
	}
	return false
}

// failedRestoreRecord is the row a Kubernetes restore leaves if it fails before
// registering its copy and then cannot remove what it created.
func failedRestoreRecord(src *domain.DatabaseInstance, req domain.RestoreRequest, projectID, namespace string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: req.NewProjectName, OrgID: src.OrgID, OwnerID: src.OwnerID,
		DBType: src.DBType, Tier: src.Tier, DeploymentMode: domain.ModeK8s, Namespace: namespace,
		RestoredFromProjectID: src.ProjectID, CreatedAt: &domain.FlexTime{Time: time.Now()},
	}
}

// keepFailedRestoreOnRecord leaves the copy FAILED with the rollback's results.
// The row is rewritten only if this restore created it: an id another project
// holds is why some restores get here. Without a namespace there is nothing the
// sweep could act on and no row to write, so the log is the only record.
func keepFailedRestoreOnRecord(store storage.InstanceStore, record *domain.DatabaseInstance, results []provisioner.CleanupResult) {
	encoded, err := json.Marshal(results)
	if err != nil {
		log.Printf("ERROR: restore %s: marshal rollback log: %v", record.ProjectID, err)
		return
	}
	created := false
	for _, result := range results {
		created = created || result.Name == unverifiedRowCleanup
	}
	if created {
		markRestoredRowFailed(store, record.ProjectID, string(encoded))
		return
	}
	if record.Namespace == "" {
		log.Printf("ERROR: restore %s left resources no project row records: %s", record.ProjectID, encoded)
		return
	}
	row := record.Clone()
	row.Status, row.CurrentStage = string(domain.StageFailed), domain.StageFailed
	row.FailureReason, row.RollbackLog = failedRestoreReason, string(encoded)
	if err := store.Create(row); err != nil {
		log.Printf("ERROR: restore %s left resources in %s and could not record them: %v", record.ProjectID, record.Namespace, err)
	}
}

func markRestoredRowFailed(store storage.InstanceStore, projectID, rollbackLog string) {
	row, err := store.FindByProjectID(projectID)
	if err != nil || row == nil {
		log.Printf("ERROR: restore %s: its row cannot be read to record the failed rollback: %v", projectID, err)
		return
	}
	failed := row.Clone()
	failed.Status, failed.CurrentStage = string(domain.StageFailed), domain.StageFailed
	failed.FailureReason, failed.RollbackLog = failedRestoreReason, rollbackLog
	// The copy is ours to remove; the source's protection does not bind that.
	failed.DeletionProtection = nil
	if err := store.UpdateIfStatus(failed, row.Status); err != nil {
		log.Printf("ERROR: restore %s: record the failed rollback: %v", projectID, err)
	}
}
