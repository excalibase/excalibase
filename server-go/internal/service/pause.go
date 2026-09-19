package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Named steps of the pause pipeline. They are written onto the project row
// as each one starts, so a pause that stops halfway says where.
const (
	pauseStepStopReplication = "STOP_REPLICATION"
	pauseStepBackup          = "PRE_PAUSE_BACKUP"
	pauseStepStopWorkload    = "STOP_WORKLOAD"
	resumeStepStartWorkload  = "START_WORKLOAD"
	resumeStepReplication    = "RESTART_REPLICATION"
)

// Backup statuses the adapters report. IN_PROGRESS is neither, so the wait
// keeps going until one of these or the budget runs out.
const (
	backupStatusCompleted = "COMPLETED"
	backupStatusFailed    = "FAILED"
)

// defaultPauseTimeout bounds each observed wait inside a pause: the pre-pause
// backup and the workload stop. A pause of a busy project takes minutes.
const (
	defaultPauseTimeout = 10 * time.Minute
	defaultPausePoll    = 5 * time.Second
)

// PauseService transitions a project ACTIVE → PAUSING → PAUSED, and the
// reverse on Resume. Every stage is observed rather than requested: the
// pre-pause backup must be seen COMPLETED and the workload must be seen
// stopped before the project is recorded PAUSED. A project is never PAUSED
// on the strength of an accepted request.
type PauseService struct {
	instances   storage.InstanceStore
	pausers     map[domain.DeploymentMode]provisioner.Pauser
	backups     PrePauseBackup
	replication ReplicationRestarter
	poller      provisioner.Poller

	mu sync.Mutex
}

// PrePauseBackup is the slice of BackupService a pause depends on: whether
// the project has anywhere to write a backup, how to start one, and how to
// watch it to completion.
type PrePauseBackup interface {
	// BackupsConfigured reports whether the project has backup storage. A
	// project without it has no backup to take and none to wait for.
	BackupsConfigured(projectID string) (bool, error)
	TriggerManualBackup(ctx context.Context, projectID string) (map[string]interface{}, error)
	// BackupStatus reports one backup's current status.
	BackupStatus(ctx context.Context, projectID, backupID string) (string, error)
}

// ReplicationRestarter brings the tenant's CDC watcher back after a resume.
// Stopping it is part of the pause (its replication session holds the
// database's shutdown open); restarting it needs the project's credentials,
// which the control plane holds, not the provisioner.
type ReplicationRestarter interface {
	RestartReplication(ctx context.Context, inst *domain.DatabaseInstance) error
}

// PauseServiceConfig wires the dependencies. Instances and Pausers are
// required; Backups, Replication and Poller have working defaults.
type PauseServiceConfig struct {
	Instances   storage.InstanceStore
	Pausers     map[domain.DeploymentMode]provisioner.Pauser
	Backups     PrePauseBackup
	Replication ReplicationRestarter
	Poller      provisioner.Poller
}

// NewPausePoller bounds a pause's observed waits on the wall clock, at the
// package's poll interval.
func NewPausePoller(timeout time.Duration) provisioner.Poller {
	return provisioner.NewPoller(defaultPausePoll, timeout)
}

func NewPauseService(c PauseServiceConfig) *PauseService {
	poller := c.Poller
	if poller.Timeout == 0 {
		poller = provisioner.NewPoller(defaultPausePoll, defaultPauseTimeout)
	}
	return &PauseService{
		instances:   c.Instances,
		pausers:     c.Pausers,
		backups:     c.Backups,
		replication: c.Replication,
		poller:      poller,
	}
}

// ErrPauseUnsupported is returned when a project's deployment mode has no
// Pauser registered (the operator owns the DB lifecycle, we don't touch it).
var ErrPauseUnsupported = errors.New("pause not supported for this deployment mode")

// ErrPauseBackupNotCompleted is what a caller is told when the pre-pause
// backup did not finish. The project is left running: pausing a database
// whose backup failed would take away the recovery point it was meant to
// have.
var ErrPauseBackupNotCompleted = errors.New("pause cancelled: the pre-pause backup did not complete; the project is still running")

// ErrPauseNotObserved is what a caller is told when the workload was asked
// to stop but was not seen stopped. The project stays in PAUSING and the
// same call retried converges.
var ErrPauseNotObserved = errors.New("pause did not complete: the project's database was not confirmed stopped; retry to continue")

// ErrResumeNotObserved is what a caller is told when a resume did not reach
// a fully working project. The project stays in RESUMING and a retry
// converges.
var ErrResumeNotObserved = errors.New("resume did not complete: the project was not confirmed running; retry to continue")

// Pause takes the project to PAUSED through observed stages: replication
// stops, the pre-pause backup completes, the workload stops, and only then
// is PAUSED recorded. Idempotent for an already-PAUSED project; a project
// left in PAUSING by a previous attempt is retried from the top.
func (s *PauseService) Pause(ctx context.Context, projectID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Status == string(domain.StatusPaused) {
		return nil
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}
	if err := s.enterPausing(inst, reason); err != nil {
		return err
	}

	step, err := s.runPause(ctx, pauser, inst)
	if err != nil {
		return s.failPause(inst, step, err)
	}
	return s.markPaused(inst)
}

// runPause performs the ordered stages, returning the step that failed.
func (s *PauseService) runPause(ctx context.Context, pauser provisioner.Pauser, inst *domain.DatabaseInstance) (string, error) {
	if err := pauser.StopReplication(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return pauseStepStopReplication, err
	}
	if err := s.awaitPrePauseBackup(ctx, inst.ProjectID); err != nil {
		return pauseStepBackup, err
	}
	if err := pauser.Pause(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return pauseStepStopWorkload, err
	}
	return "", nil
}

// awaitPrePauseBackup starts the pre-pause backup and returns only once it
// is observed COMPLETED. A project with no backup storage takes no backup:
// there is nothing to write and nothing to wait for, which is an explicit
// branch rather than a silently skipped step.
func (s *PauseService) awaitPrePauseBackup(ctx context.Context, projectID string) error {
	if s.backups == nil {
		return nil
	}
	configured, err := s.backups.BackupsConfigured(projectID)
	if err != nil {
		return fmt.Errorf("resolve backup storage: %w", err)
	}
	if !configured {
		log.Printf("pause %s: no backup storage configured; pausing without a pre-pause backup", projectID)
		return nil
	}
	started, err := s.backups.TriggerManualBackup(ctx, projectID)
	if err != nil {
		return fmt.Errorf("trigger pre-pause backup: %w", err)
	}
	backupID, _ := started["id"].(string)
	if backupID == "" {
		return errors.New("pre-pause backup was accepted without an id; it cannot be followed to completion")
	}
	// The backup is "outstanding" until it reports COMPLETED; a FAILED one
	// is not outstanding, it is over, so it ends the wait as an error.
	return s.poller.WaitUntilClear(ctx, "pre-pause backup "+backupID, func(ctx context.Context) ([]string, error) {
		status, err := s.backups.BackupStatus(ctx, projectID, backupID)
		if err != nil {
			return nil, fmt.Errorf("read backup status: %w", err)
		}
		switch status {
		case backupStatusCompleted:
			return nil, nil
		case backupStatusFailed:
			return nil, fmt.Errorf("backup %s reported %s", backupID, status)
		default:
			return []string{status}, nil
		}
	})
}

// enterPausing records the in-flight state. The one-way DELETING door is a
// stop signal: a teardown that claimed the project owns its resources now.
func (s *PauseService) enterPausing(inst *domain.DatabaseInstance, reason string) error {
	inst.Status = string(domain.StatusPausing)
	inst.PauseReason = reason
	inst.CurrentStep = pauseStepStopReplication
	inst.FailureReason = ""
	return s.persist(inst)
}

// failPause leaves a truthful row: still PAUSING, naming the step it stopped
// on and a reason safe to show a caller. The retry starts again from the top
// and converges. A pre-pause backup that did not complete is different — the
// project was never stopped, so it goes back to being a running project.
func (s *PauseService) failPause(inst *domain.DatabaseInstance, step string, cause error) error {
	if errors.Is(cause, storage.ErrProjectDeleting) {
		return cause
	}
	clientErr := ErrPauseNotObserved
	if step == pauseStepBackup {
		clientErr = ErrPauseBackupNotCompleted
		inst.Status = "ACTIVE"
		inst.PauseReason = ""
	}
	log.Printf("pause %s stopped at %s: %v", inst.ProjectID, step, cause)
	inst.CurrentStep = step
	inst.FailureReason = clientErr.Error()
	if err := s.persist(inst); err != nil {
		return err
	}
	return clientErr
}

func (s *PauseService) markPaused(inst *domain.DatabaseInstance) error {
	inst.Status = string(domain.StatusPaused)
	inst.CurrentStep = ""
	inst.FailureReason = ""
	return s.persist(inst)
}

// Resume takes a PAUSED project back to ACTIVE: the workload starts and is
// waited out by the provisioner, then replication is restarted. Idempotent
// for any project that is not PAUSED.
func (s *PauseService) Resume(ctx context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Status != string(domain.StatusPaused) {
		return nil
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}

	inst.Status = string(domain.StatusResuming)
	inst.CurrentStep = resumeStepStartWorkload
	inst.FailureReason = ""
	if err := s.persist(inst); err != nil {
		return err
	}

	step, err := s.runResume(ctx, pauser, inst)
	if err != nil {
		return s.failResume(inst, step, err)
	}
	inst.Status = "ACTIVE"
	inst.PauseReason = ""
	inst.CurrentStep = ""
	inst.FailureReason = ""
	inst.LastActiveAt = &domain.FlexTime{Time: time.Now()}
	return s.persist(inst)
}

// runResume starts the workload and, once the provisioner reports a ready
// primary, brings replication back. Restarting the watcher any earlier would
// point it at a database that is not accepting connections yet.
func (s *PauseService) runResume(ctx context.Context, pauser provisioner.Pauser, inst *domain.DatabaseInstance) (string, error) {
	if err := pauser.Resume(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return resumeStepStartWorkload, err
	}
	if s.replication == nil {
		return "", nil
	}
	if err := s.replication.RestartReplication(ctx, inst); err != nil {
		return resumeStepReplication, err
	}
	return "", nil
}

func (s *PauseService) failResume(inst *domain.DatabaseInstance, step string, cause error) error {
	if errors.Is(cause, storage.ErrProjectDeleting) {
		return cause
	}
	log.Printf("resume %s stopped at %s: %v", inst.ProjectID, step, cause)
	inst.CurrentStep = step
	inst.FailureReason = ErrResumeNotObserved.Error()
	if err := s.persist(inst); err != nil {
		return err
	}
	return ErrResumeNotObserved
}

// persist writes the row, treating the one-way DELETING door as a stop
// signal for whatever the caller was in the middle of.
func (s *PauseService) persist(inst *domain.DatabaseInstance) error {
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	if err := s.instances.Update(inst); err != nil {
		if errors.Is(err, storage.ErrProjectDeleting) {
			return err
		}
		return fmt.Errorf("persist %s state for %s: %w", inst.Status, inst.ProjectID, err)
	}
	return nil
}
