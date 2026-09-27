package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	// DeletionGracePeriod is how long a deleted project is kept stopped, with
	// its disk, before the sweep hard-deletes it (owner decision 2026-09-28).
	DeletionGracePeriod = 7 * 24 * time.Hour
	// RetainedBackupPeriod is how long a deleted project's kept backups stay
	// in the object store after the hard delete (owner decision 2026-09-28).
	RetainedBackupPeriod = 14 * 24 * time.Hour
	// PauseReasonDeletion is the pause reason a scheduled deletion records.
	PauseReasonDeletion = "deletion"
)

var (
	// ErrDeletionGraceUnavailable means the project cannot be stopped for its
	// grace period because no pause service is wired. Nothing is deleted.
	ErrDeletionGraceUnavailable = errors.New("project deletion needs the pause service, which is not configured")
	// ErrNotScheduledForDeletion is returned when a cancel names a project
	// that is not in its deletion grace period.
	ErrNotScheduledForDeletion = errors.New("project is not scheduled for deletion")
	// ErrScheduledForDeletion is returned when a change is asked of a project
	// in its deletion grace period.
	ErrScheduledForDeletion = errors.New("project is scheduled for deletion; cancel the deletion first")
	// ErrDeletionStopFailed means the project could not be stopped for its
	// grace period, so it was not scheduled and nothing was deleted.
	ErrDeletionStopFailed = errors.New("project could not be stopped for deletion")
	// ErrDeletionNotDue is what the sweep gets for a project that was listed
	// as due but, re-read under the lease, no longer is.
	ErrDeletionNotDue = errors.New("project deletion is not due")
)

// DeletionPauser stops a project's workload with a backup first. PauseService
// implements it.
type DeletionPauser interface {
	Pause(ctx context.Context, projectID, reason string) error
}

// SetDeletionPauser wires the pause a scheduled deletion stops the project with.
func (s *ProvisioningService) SetDeletionPauser(p DeletionPauser) { s.deletionPauser = p }

// SetRetainedBackupStore wires the record of deleted projects' kept backups.
func (s *ProvisioningService) SetRetainedBackupStore(r storage.RetainedBackupStore) {
	s.retainedBackups = r
}

// SetDeletionClock replaces time.Now for the grace and retention dates.
func (s *ProvisioningService) SetDeletionClock(now func() time.Time) { s.deletionNow = now }

func (s *ProvisioningService) deletionClock() time.Time {
	if s.deletionNow != nil {
		return s.deletionNow()
	}
	return time.Now()
}

// holdsCustomerData reports whether a project in this status has a database
// worth a grace period. Anything else (FAILED, RESTORING, a teardown already
// running) is deleted at once, as before. A RESUMING project is not paused by
// the pause service, so its schedule is refused until it settles.
func holdsCustomerData(status string) bool {
	switch status {
	case string(domain.StatusActive), string(domain.StatusPaused), string(domain.StatusPausing),
		string(domain.StatusResuming):
		return true
	}
	return false
}

func isProtected(inst *domain.DatabaseInstance) bool {
	return inst.DeletionProtection != nil && *inst.DeletionProtection
}

// ScheduleDeletion is what DELETE does. A project holding data is stopped
// (backup first, disk kept) and marked PENDING_DELETION until the grace
// period ends; any other project is deleted at once. It returns the scheduled
// row, or nil when the project was deleted.
func (s *ProvisioningService) ScheduleDeletion(ctx context.Context, projectID string, opts DeprovisionOptions) (*domain.DatabaseInstance, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if inst.Status == string(domain.StatusPendingDeletion) {
		return s.updatePendingChoice(ctx, inst, opts)
	}
	if !holdsCustomerData(inst.Status) {
		return nil, s.DeprovisionWithOptions(ctx, projectID, opts)
	}
	if isProtected(inst) {
		return nil, fmt.Errorf("%w for %s", ErrDeletionProtected, projectID)
	}
	if opts.DeleteBackups != nil && *opts.DeleteBackups && s.backupPurger == nil {
		return nil, ErrBackupPurgeNotConfigured
	}
	if s.deletionPauser == nil {
		return nil, ErrDeletionGraceUnavailable
	}
	// The pause waits for a backup; a caller hanging up must not leave the
	// project half paused. The pause bounds itself (EXCALIBASE_PAUSE_TIMEOUT).
	ctx = context.WithoutCancel(ctx)
	if err := s.deletionPauser.Pause(ctx, projectID, PauseReasonDeletion); err != nil {
		if errors.Is(err, ErrPauseUnsupported) {
			return nil, fmt.Errorf("%w: %w", ErrDeletionGraceUnavailable, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrDeletionStopFailed, err)
	}
	return s.markPendingDeletion(ctx, projectID, opts)
}

// holdLifecycle takes the per-project lease deletion, pause and resume share.
func (s *ProvisioningService) holdLifecycle(ctx context.Context, projectID string) (func(), error) {
	release, claimed, err := s.claimer().Claim(ctx, projectID, OperationDeletion)
	if err != nil {
		return nil, fmt.Errorf("claim project %s: %w", projectID, err)
	}
	if !claimed {
		return nil, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	return release, nil
}

func (s *ProvisioningService) markPendingDeletion(ctx context.Context, projectID string, opts DeprovisionOptions) (*domain.DatabaseInstance, error) {
	release, err := s.holdLifecycle(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer release()
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if inst.Status != string(domain.StatusPaused) {
		return nil, fmt.Errorf("%w: %s is %s after the pause", storage.ErrProjectStatusChanged, projectID, inst.Status)
	}
	if isProtected(inst) {
		return nil, fmt.Errorf("%w for %s", ErrDeletionProtected, projectID)
	}
	now := s.deletionClock()
	inst.Status = string(domain.StatusPendingDeletion)
	inst.CurrentStage = domain.StatusPendingDeletion
	inst.CurrentStep = ""
	inst.DeletionDeleteBackups = opts.DeleteBackups != nil && *opts.DeleteBackups
	inst.DeletionScheduledAt = &domain.FlexTime{Time: now}
	inst.DeletionDueAt = &domain.FlexTime{Time: now.Add(DeletionGracePeriod)}
	inst.UpdatedAt = &domain.FlexTime{Time: now}
	if err := s.store.UpdateIfStatus(inst, string(domain.StatusPaused)); err != nil {
		return nil, err
	}
	log.Printf("action=schedule_deletion project=%s due=%s delete_backups=%t",
		projectID, inst.DeletionDueAt.Time.Format(time.RFC3339), inst.DeletionDeleteBackups)
	return inst, nil
}

// updatePendingChoice answers a repeated DELETE: the due date stands, and an
// explicit backup choice replaces the recorded one.
func (s *ProvisioningService) updatePendingChoice(ctx context.Context, inst *domain.DatabaseInstance, opts DeprovisionOptions) (*domain.DatabaseInstance, error) {
	if opts.DeleteBackups == nil || *opts.DeleteBackups == inst.DeletionDeleteBackups {
		return inst, nil
	}
	if *opts.DeleteBackups && s.backupPurger == nil {
		return nil, ErrBackupPurgeNotConfigured
	}
	release, err := s.holdLifecycle(ctx, inst.ProjectID)
	if err != nil {
		return nil, err
	}
	defer release()
	if inst, err = s.store.FindByProjectID(inst.ProjectID); err != nil || inst == nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	if inst.Status != string(domain.StatusPendingDeletion) {
		return nil, fmt.Errorf("%w: %s is %s", storage.ErrProjectStatusChanged, inst.ProjectID, inst.Status)
	}
	inst.DeletionDeleteBackups = *opts.DeleteBackups
	if err := s.store.UpdateIfStatus(inst, string(domain.StatusPendingDeletion)); err != nil {
		return nil, err
	}
	return inst, nil
}

// CancelDeletion ends a project's grace period. The project stays PAUSED, with
// its disk, and deletion protection is turned back on.
func (s *ProvisioningService) CancelDeletion(ctx context.Context, projectID string) error {
	if inst, err := s.store.FindByProjectID(projectID); err != nil || inst == nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	release, err := s.holdLifecycle(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if inst.Status != string(domain.StatusPendingDeletion) {
		return fmt.Errorf("%w: %s is %s", ErrNotScheduledForDeletion, projectID, inst.Status)
	}
	inst.Status = string(domain.StatusPaused)
	inst.CurrentStage = domain.StatusPaused
	inst.DeletionScheduledAt = nil
	inst.DeletionDueAt = nil
	inst.DeletionDeleteBackups = false
	inst.DeletionProtection = boolPtr(true)
	inst.UpdatedAt = &domain.FlexTime{Time: s.deletionClock()}
	if err := s.store.UpdateIfStatus(inst, string(domain.StatusPendingDeletion)); err != nil {
		return err
	}
	log.Printf("action=cancel_deletion project=%s", projectID)
	return nil
}

// DeletionSweepReport lists what one pass of RunDueDeletions did.
type DeletionSweepReport struct {
	Deleted []string
	Failed  []string
}

// RunDueDeletions hard-deletes every project whose grace period has ended,
// and retries a scheduled deletion whose teardown stopped part way.
func (s *ProvisioningService) RunDueDeletions(ctx context.Context) DeletionSweepReport {
	var report DeletionSweepReport
	all, err := s.store.FindAll()
	if err != nil {
		log.Printf("deletion sweep: list projects: %v", err)
		return report
	}
	now := s.deletionClock()
	for _, inst := range all {
		if !deletionDue(inst, now) {
			continue
		}
		err := s.DeprovisionWithOptions(ctx, inst.ProjectID, DeprovisionOptions{dueAt: &now})
		switch {
		case err == nil:
			report.Deleted = append(report.Deleted, inst.ProjectID)
		case errors.Is(err, ErrProjectOperationRunning), errors.Is(err, ErrProjectNotFound),
			errors.Is(err, ErrDeletionNotDue):
			// Another replica or a caller holds it, it is already gone, or it
			// was cancelled or rescheduled since the list was read.
		default:
			log.Printf("deletion sweep: %s: %v", inst.ProjectID, err)
			report.Failed = append(report.Failed, inst.ProjectID)
		}
	}
	return report
}

// deletionDue reports whether the sweep owes this project its hard delete: its
// grace period is over, or a teardown the schedule started has stopped
// (including at the backup purge, which no owner is left to retry).
func deletionDue(inst *domain.DatabaseInstance, now time.Time) bool {
	if inst.DeletionDueAt == nil || inst.DeletionDueAt.Time.After(now) {
		return false
	}
	return inst.Status == string(domain.StatusPendingDeletion) || domain.IsDeletionStatus(inst.Status)
}

// recordRetainedBackups is the teardown step that dates a kept backup set for
// purging. Without a backup target no backups were ever written, so there is
// nothing to record.
func (s *ProvisioningService) recordRetainedBackups(_ context.Context, inst *domain.DatabaseInstance) error {
	if _, ok := s.BackupStorage(); !ok {
		return nil
	}
	if s.backupPurger == nil {
		return fmt.Errorf("record retained backups for %s: %w", inst.ProjectID, ErrBackupPurgeNotConfigured)
	}
	now := s.deletionClock()
	return s.retainedBackups.RecordRetainedBackups(storage.RetainedBackup{
		ProjectID:      inst.ProjectID,
		OrgID:          inst.OrgID,
		DeploymentMode: inst.DeploymentMode,
		DeletedAt:      now,
		PurgeAfter:     now.Add(RetainedBackupPeriod),
	})
}

// RetainedBackupSweepReport lists what one pass of PurgeDueRetainedBackups did.
type RetainedBackupSweepReport struct {
	Purged []string
	Failed []string
}

// PurgeDueRetainedBackups deletes the kept backups of every deleted project
// whose retention has ended. A failed purge keeps its record for the next pass.
func (s *ProvisioningService) PurgeDueRetainedBackups(ctx context.Context) RetainedBackupSweepReport {
	var report RetainedBackupSweepReport
	if s.retainedBackups == nil {
		return report
	}
	due, err := s.retainedBackups.DueRetainedBackups(s.deletionClock())
	if err != nil {
		log.Printf("retained backup sweep: list: %v", err)
		return report
	}
	for _, kept := range due {
		if err := s.purgeRetained(ctx, kept); err != nil {
			log.Printf("retained backup sweep: %s: %v", kept.ProjectID, err)
			report.Failed = append(report.Failed, kept.ProjectID)
			continue
		}
		report.Purged = append(report.Purged, kept.ProjectID)
	}
	return report
}

func (s *ProvisioningService) purgeRetained(ctx context.Context, kept storage.RetainedBackup) error {
	if s.backupPurger == nil {
		return ErrBackupPurgeNotConfigured
	}
	gone := &domain.DatabaseInstance{ProjectID: kept.ProjectID, DeploymentMode: kept.DeploymentMode}
	deleted, err := s.backupPurger.Purge(ctx, gone)
	if err != nil && !errors.Is(err, ErrNoBackupsForMode) {
		return err
	}
	log.Printf("action=purge_retained_backups project=%s deleted=%d", kept.ProjectID, deleted)
	return s.retainedBackups.DeleteRetainedBackups(kept.ProjectID)
}
