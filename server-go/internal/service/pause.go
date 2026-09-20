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
	// pauseStepRestoreReplication is the repair a failed pause runs when it
	// had already stopped the watcher on a database that is still up.
	pauseStepRestoreReplication = "RESTORE_REPLICATION"
	// pauseStepWithdrawEndpoint removes the project's public database
	// endpoint. A paused project must refuse connections rather than
	// publish a port that points at a database which is not running.
	pauseStepWithdrawEndpoint = "WITHDRAW_PUBLIC_ENDPOINT"
	resumeStepStartWorkload   = "START_WORKLOAD"
	resumeStepReplication     = "RESTART_REPLICATION"
	// resumeStepPublishEndpoint brings the endpoint back on the port the
	// project still holds, so a customer's saved connection string works
	// again unchanged.
	resumeStepPublishEndpoint = "PUBLISH_PUBLIC_ENDPOINT"
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
	// claimer serialises lifecycle operations per project across replicas.
	// The process-local mutex below only orders the goroutines in THIS
	// process; the lease is what stops two replicas interleaving.
	claimer ProjectOperationClaimer
	// endpoints withdraws and republishes the project's public database
	// endpoint. Nil when the platform offers none.
	endpoints PublicEndpointReconciler

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
	// LatestBackupID names the project's newest backup, so a pause can tell
	// whether the one it recorded is still the recovery point it would take
	// now. Empty means the project has none.
	LatestBackupID(ctx context.Context, projectID string) (string, error)
}

// pauseBackupReuseWindow is how long a pre-pause backup stays the recovery
// point for the episode that took it. A day: long enough that a pause stuck
// on a broken hibernation for hours does not re-dump the database on every
// retry, short enough that a project which has been accepting writes all day
// is not paused behind a stale backup.
const pauseBackupReuseWindow = 24 * time.Hour

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
	// Claimer must be the SAME claimer the provisioning service holds, or
	// pause and deletion will not exclude one another. Defaults to an
	// in-process claimer, which is correct for a single replica.
	Claimer ProjectOperationClaimer
	// Endpoints reconciles the project's public database endpoint across
	// the pause. Optional: a platform with no public endpoints wires none.
	Endpoints PublicEndpointReconciler
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
	claimer := c.Claimer
	if claimer == nil {
		claimer = newInProcessOperationClaimer()
	}
	return &PauseService{
		claimer:     claimer,
		instances:   c.Instances,
		pausers:     c.Pausers,
		backups:     c.Backups,
		replication: c.Replication,
		poller:      poller,
		endpoints:   c.Endpoints,
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

// hold takes the project's lifecycle lease for the length of one operation.
// The returned release is safe to defer: it runs on every path out,
// including a panic, so a crashed step cannot leave a project unusable.
//
// A project another operation holds is busy, not broken — the caller is told
// to retry rather than being allowed to interleave with a shutdown that is
// halfway done.
func (s *PauseService) hold(ctx context.Context, projectID string, op ProjectOperation) (func(), error) {
	release, claimed, err := s.claimer.Claim(ctx, projectID, op)
	if err != nil {
		return nil, fmt.Errorf("claim project for %s: %w", op, err)
	}
	if !claimed {
		return nil, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	return release, nil
}

// pausable reports whether a pause has work to do. ACTIVE is the ordinary
// case; PAUSING is a pause that failed part way, and running it again is how
// it converges. Everything else — PAUSED, RESUMING, a teardown, a build — is
// either already there or not a pause's to touch.
func pausable(status string) bool {
	return status == "ACTIVE" || status == string(domain.StatusPausing)
}

// resumable decides whether a resume has work to do, from observation rather
// than from the label. PAUSED and RESUMING are plainly resumable. PAUSING is
// the interesting one: a pause that failed after the database was already
// down leaves that label on a project whose workload is stopped, and without
// asking the provisioner there is no way to tell it from a pause that failed
// before anything was stopped — which must not be "resumed" into starting a
// database that never went away.
func (s *PauseService) resumable(ctx context.Context, pauser provisioner.Pauser, inst *domain.DatabaseInstance) (bool, error) {
	switch inst.Status {
	case string(domain.StatusPaused), string(domain.StatusResuming):
		return true, nil
	case string(domain.StatusPausing):
		stopped, err := pauser.WorkloadStopped(ctx, inst.Namespace, inst.ProjectID)
		if err != nil {
			return false, fmt.Errorf("read workload state for %s: %w", inst.ProjectID, err)
		}
		return stopped, nil
	default:
		return false, nil
	}
}

// Pause takes the project to PAUSED through observed stages: replication
// stops, the pre-pause backup completes, the workload stops, and only then
// is PAUSED recorded. Idempotent for an already-PAUSED project; a project
// left in PAUSING by a previous attempt is retried from the top.
func (s *PauseService) Pause(ctx context.Context, projectID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.hold(ctx, projectID, OperationPause)
	if err != nil {
		return err
	}
	defer release()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}
	if domain.IsDeletionStatus(inst.Status) {
		// The one-way door: a teardown owns the project's resources now, so
		// the caller is told rather than quietly ignored.
		return storage.ErrProjectDeleting
	}
	if !pausable(inst.Status) {
		// PAUSED is already there; anything else is a state a pause has no
		// business rewriting.
		return nil
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
//
// The backup goes first. The watcher does not hold up a backup — only the
// smart shutdown — so stopping replication earlier would buy nothing and
// would leave a running project without CDC for the whole length of a
// backup, which is the step most likely to fail. Replication stops only once
// there is a recovery point and the shutdown is the next thing to happen.
func (s *PauseService) runPause(ctx context.Context, pauser provisioner.Pauser, inst *domain.DatabaseInstance) (string, error) {
	if err := s.awaitPrePauseBackup(ctx, inst); err != nil {
		return pauseStepBackup, err
	}
	if err := pauser.StopReplication(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return pauseStepStopReplication, err
	}
	if err := pauser.Pause(ctx, inst.Namespace, inst.ProjectID); err != nil {
		// The database is still up and its watcher is gone: put replication
		// back before reporting, or the project runs on with its slot
		// retaining WAL that nothing consumes.
		return s.repairReplication(ctx, inst, pauseStepStopWorkload, err)
	}
	// The database is observed stopped, so the endpoint now fronts nothing.
	// It goes last: a pause that failed earlier leaves a running project
	// still reachable on the port its customers have saved.
	if s.endpoints != nil {
		if err := s.endpoints.Withdraw(ctx, inst); err != nil {
			return pauseStepWithdrawEndpoint, err
		}
	}
	return "", nil
}

// repairReplication restarts the watcher a failed pause had already stopped,
// and reports which failure the caller should be told about. The original
// cause wins when the repair succeeds; a repair that itself fails is the
// more serious of the two, because it leaves a running project with
// replication off and that has to be visible and retryable.
func (s *PauseService) repairReplication(ctx context.Context, inst *domain.DatabaseInstance, step string, cause error) (string, error) {
	if s.replication == nil {
		return step, cause
	}
	if err := s.replication.RestartReplication(ctx, inst); err != nil {
		log.Printf("pause %s: %v; restarting replication also failed: %v", inst.ProjectID, cause, err)
		return pauseStepRestoreReplication, err
	}
	return step, cause
}

// awaitPrePauseBackup starts the pre-pause backup and returns only once it
// is observed COMPLETED. A project with no backup storage takes no backup:
// there is nothing to write and nothing to wait for, which is an explicit
// branch rather than a silently skipped step.
func (s *PauseService) awaitPrePauseBackup(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.backups == nil {
		return nil
	}
	projectID := inst.ProjectID
	configured, err := s.backups.BackupsConfigured(projectID)
	if err != nil {
		return fmt.Errorf("resolve backup storage: %w", err)
	}
	if !configured {
		log.Printf("pause %s: no backup storage configured; pausing without a pre-pause backup", projectID)
		return nil
	}
	backupID, err := s.episodeBackup(ctx, inst)
	if err != nil {
		return err
	}
	if backupID == "" {
		started, err := s.backups.TriggerManualBackup(ctx, projectID)
		if err != nil {
			return fmt.Errorf("trigger pre-pause backup: %w", err)
		}
		backupID, _ = started["id"].(string)
		if backupID == "" {
			return errors.New("pre-pause backup was accepted without an id; it cannot be followed to completion")
		}
		// Record it before waiting. A wait that times out leaves the backup
		// running, and the retry has to observe THAT one rather than file a
		// second beside it.
		if err := s.rememberEpisodeBackup(inst, backupID); err != nil {
			return err
		}
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

// episodeBackup returns the backup already taken for the pause episode in
// flight, or "" when a fresh one is needed. It is reused only while it is
// still the project's newest backup and still inside the reuse window —
// anything else is not the recovery point a pause now would produce.
func (s *PauseService) episodeBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	if inst.PauseBackupID == "" || inst.PauseBackupAt == nil {
		return "", nil
	}
	if time.Since(inst.PauseBackupAt.Time) >= pauseBackupReuseWindow {
		log.Printf("pause %s: the episode's backup %s is older than %s; taking a fresh one",
			inst.ProjectID, inst.PauseBackupID, pauseBackupReuseWindow)
		return "", nil
	}
	latest, err := s.backups.LatestBackupID(ctx, inst.ProjectID)
	if err != nil {
		return "", fmt.Errorf("read latest backup: %w", err)
	}
	if latest != inst.PauseBackupID {
		log.Printf("pause %s: the episode's backup %s is no longer the newest; taking a fresh one",
			inst.ProjectID, inst.PauseBackupID)
		return "", nil
	}
	return inst.PauseBackupID, nil
}

// rememberEpisodeBackup records the backup this pause episode is waiting on,
// pinned to PAUSING so it cannot land on a project something else moved.
func (s *PauseService) rememberEpisodeBackup(inst *domain.DatabaseInstance, backupID string) error {
	inst.PauseBackupID = backupID
	inst.PauseBackupAt = &domain.FlexTime{Time: time.Now()}
	return s.persistFrom(inst, string(domain.StatusPausing))
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
	if err := s.persistFrom(inst, string(domain.StatusPausing)); err != nil {
		return err
	}
	return clientErr
}

func (s *PauseService) markPaused(inst *domain.DatabaseInstance) error {
	inst.Status = string(domain.StatusPaused)
	inst.CurrentStep = ""
	inst.FailureReason = ""
	clearPauseRetryBackoff(inst)
	return s.persistFrom(inst, string(domain.StatusPausing))
}

// clearPauseRetryBackoff forgets how many attempts it took. The project has
// settled, so the next time it needs pausing it starts from a clean slate
// rather than inheriting a backoff grown by an unrelated failure. It rides
// the ordinary Update, which already refuses a row a teardown owns.
func clearPauseRetryBackoff(inst *domain.DatabaseInstance) {
	inst.PauseAttempts = 0
	inst.PauseLastAttemptAt = nil
	// The pause episode is over too: the next one takes its own backup
	// rather than leaning on a recovery point from an old attempt.
	inst.PauseBackupID = ""
	inst.PauseBackupAt = nil
}

// Resume takes a PAUSED project back to ACTIVE: the workload starts and is
// waited out by the provisioner, then replication is restarted. Idempotent
// for any project that is not PAUSED.
func (s *PauseService) Resume(ctx context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.hold(ctx, projectID, OperationResume)
	if err != nil {
		return err
	}
	defer release()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}
	resumable, err := s.resumable(ctx, pauser, inst)
	if err != nil {
		return err
	}
	if !resumable {
		return nil
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
	clearPauseRetryBackoff(inst)
	inst.LastActiveAt = &domain.FlexTime{Time: time.Now()}
	return s.persistFrom(inst, string(domain.StatusResuming))
}

// runResume starts the workload and, once the provisioner reports a ready
// primary, brings replication back. Restarting the watcher any earlier would
// point it at a database that is not accepting connections yet.
func (s *PauseService) runResume(ctx context.Context, pauser provisioner.Pauser, inst *domain.DatabaseInstance) (string, error) {
	if err := pauser.Resume(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return resumeStepStartWorkload, err
	}
	if s.replication != nil {
		if err := s.replication.RestartReplication(ctx, inst); err != nil {
			return resumeStepReplication, err
		}
	}
	// The primary is ready, so the endpoint has something to front again.
	if s.endpoints != nil {
		if err := s.endpoints.Publish(ctx, inst); err != nil {
			return resumeStepPublishEndpoint, err
		}
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
	if err := s.persistFrom(inst, string(domain.StatusResuming)); err != nil {
		return err
	}
	return ErrResumeNotObserved
}

// persist writes the row unconditionally. Used for the first write of an
// operation, where the precondition was the status this operation just read.
func (s *PauseService) persist(inst *domain.DatabaseInstance) error {
	return s.persistFrom(inst, "")
}

// persistFrom writes the row, pinned to the status this operation last wrote
// when one is given. The lease already stops two operations running at once;
// this is the backstop that makes "PAUSED over a running database" impossible
// even if the lease were bypassed — the write simply matches no row.
//
// The one-way DELETING door and a status that moved underneath are both stop
// signals, returned as they are so the caller can tell them apart.
func (s *PauseService) persistFrom(inst *domain.DatabaseInstance, expected string) error {
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	var err error
	if expected == "" {
		err = s.instances.Update(inst)
	} else {
		err = s.instances.UpdateIfStatus(inst, expected)
	}
	if err != nil {
		if errors.Is(err, storage.ErrProjectDeleting) || errors.Is(err, storage.ErrProjectStatusChanged) {
			return err
		}
		return fmt.Errorf("persist %s state for %s: %w", inst.Status, inst.ProjectID, err)
	}
	return nil
}
