package service

import (
	"context"
	"errors"
	"fmt"
	"log"

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

// registerVerifiedProject finishes a restore the honest way: the project is
// registered as RESTORING, the database is made to answer a query with the
// credentials that registration filed, and only then does the row become
// ACTIVE. Removing the unverified row is registered on pc, so a failed probe
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
	pc.RegisterCleanup("remove unverified restored project", func(context.Context) error {
		return store.Delete(projectID)
	})

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
// belong to the deletion pipeline, so nothing is compensated.
func failRestore(ctx context.Context, pc *provisioner.ProvisionContext, projectID string, err error) error {
	if errors.Is(err, ErrRestoreTargetDeleting) {
		log.Printf("restore %s: stopped, target claimed for deletion", projectID)
		return ErrRestoreTargetDeleting
	}
	results := pc.Rollback(ctx)
	log.Printf("restore %s failed: %v; compensations: %+v", projectID, err, results)
	return ErrRestoreNotObserved
}
