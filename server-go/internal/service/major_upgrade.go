package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// OperationMajorUpgrade holds a project's lifecycle lease for the whole run
// of a major upgrade (EXC-668).
const OperationMajorUpgrade ProjectOperation = "major version upgrade"

var (
	// ErrMajorUpgradeUnsupported refuses a database a major upgrade does not cover.
	ErrMajorUpgradeUnsupported = errors.New("major upgrades are not available for this database")
	// ErrMajorUpgradeTargetNotOffered refuses a major the catalogue does not offer from the current one.
	ErrMajorUpgradeTargetNotOffered = errors.New("that version is not offered as an upgrade for this database")
	// ErrMajorUpgradeNeedsBackups refuses a project with nowhere to put the backup taken first.
	ErrMajorUpgradeNeedsBackups = errors.New("a major upgrade takes a backup first; turn on backups for this project")
	// ErrMajorUpgradeBlocked is a database the checks found not ready.
	ErrMajorUpgradeBlocked = errors.New("the database is not ready for a major upgrade")
	// ErrMajorUpgradeCheckUnavailable is a check that could not run.
	ErrMajorUpgradeCheckUnavailable = errors.New("the database could not be checked right now; try again")
)

// MajorUpgradeBlockedError lists everything that must change before an upgrade.
type MajorUpgradeBlockedError struct{ Blockers []string }

func (e *MajorUpgradeBlockedError) Error() string {
	return ErrMajorUpgradeBlocked.Error() + ": " + strings.Join(e.Blockers, "; ")
}

func (e *MajorUpgradeBlockedError) Unwrap() error { return ErrMajorUpgradeBlocked }

// UpgradeOutcome is how a conversion ended. Converted: the data is on the
// new major. RolledBack: it never left the old one and the database runs on
// it again. Neither: it stopped somewhere it could not put back by itself.
type UpgradeOutcome struct {
	Converted  bool
	RolledBack bool
	// Unsettled: the conversion may still finish by itself. The run stays
	// open and recovery checks it again; nothing is put back.
	Unsettled bool
	// Public is fixed wording for the user; Cause stays in the log.
	Public string
	Cause  error
}

// UpgradeProgress is what a conversion reports while it runs.
type UpgradeProgress interface {
	Step(step string)
	WritesStopped(at time.Time)
	// Converted records that the data now lives on the new major. A
	// conversion that cannot record it must not go on as if it had.
	Converted(at time.Time) error
}

// MajorUpgrader is one deployment's half of a major upgrade.
type MajorUpgrader interface {
	// Preflight lists what keeps inst from moving to target; an error is a
	// check that could not run.
	Preflight(ctx context.Context, inst *domain.DatabaseInstance, target string) ([]string, error)
	// TakeBackup backs the database up and returns once the backup completed.
	TakeBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error)
	// Convert moves the data to run.ToVersion, putting the old major back
	// itself when it can.
	Convert(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome
	// Recover ends a run whose driver died before the conversion was recorded.
	Recover(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome
	// WaitServing waits for every copy of the converted database to serve.
	WaitServing(ctx context.Context, inst *domain.DatabaseInstance) error
	// AfterSteps refreshes statistics and extensions; it returns warnings.
	AfterSteps(ctx context.Context, inst *domain.DatabaseInstance) []string
	// Cleanup removes what the run left behind once it is over.
	Cleanup(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade)
}

// MajorUpgradeProjects is what a major upgrade needs from the project.
type MajorUpgradeProjects interface {
	// HoldForMajorUpgrade takes the project's lifecycle lease and admits the
	// row's status with admit.
	HoldForMajorUpgrade(ctx context.Context, projectID string, admit func(status string) error) (*domain.DatabaseInstance, func(), error)
	StopReplication(ctx context.Context, inst *domain.DatabaseInstance) error
	RestartReplication(ctx context.Context, inst *domain.DatabaseInstance) error
	ReapplyCredentials(ctx context.Context, inst *domain.DatabaseInstance) error
	AnnounceDatabaseReplaced(ctx context.Context, projectID string)
}

// MajorUpgradeConfig wires a MajorUpgradeService. Every field but NewID,
// Now and Observers is required.
type MajorUpgradeConfig struct {
	Runs      storage.MajorUpgradeStore
	Instances storage.InstanceStore
	Projects  MajorUpgradeProjects
	Probe     DatabaseProbe
	Engines   map[domain.DeploymentMode]MajorUpgrader
	// Backups reports whether the project's deployment has a backup store.
	Backups   func(inst *domain.DatabaseInstance) bool
	Observers []StatusObserver
	NewID     func() string
	Now       func() time.Time
}

// MajorUpgradeService runs major upgrades: checks, a forced backup, the
// deployment's conversion, then the steps that make the database whole again.
type MajorUpgradeService struct {
	cfg MajorUpgradeConfig
	// checks spares the database repeated status reads.
	checks recentChecks
	// runSync drives the run inside Start; tests read the outcome directly.
	runSync bool
}

// postUpgradeBackupAttempts bounds the retries of the first backup on the
// new major, which is what point-in-time restore starts from afterwards.
const postUpgradeBackupAttempts = 3

func NewMajorUpgradeService(cfg MajorUpgradeConfig) *MajorUpgradeService {
	if cfg.NewID == nil {
		cfg.NewID = newMajorUpgradeID
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MajorUpgradeService{cfg: cfg}
}

func newMajorUpgradeID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "mu-" + hex.EncodeToString(b)
}

// Start checks the request, takes the lease, records the run and drives it
// in the background. The returned run is the record as started.
func (s *MajorUpgradeService) Start(ctx context.Context, projectID string, req domain.MajorUpgradeRequest, actor string) (*domain.MajorUpgrade, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	inst, release, err := s.cfg.Projects.HoldForMajorUpgrade(context.WithoutCancel(ctx), projectID, func(status string) error {
		return requireActive(projectID, status)
	})
	if err != nil {
		return nil, err
	}
	engine, err := s.admit(ctx, inst, req.Target())
	if err != nil {
		release()
		return nil, err
	}
	run := &domain.MajorUpgrade{
		ID: s.cfg.NewID(), ProjectID: projectID, Engine: string(domain.PostgreSQL),
		FromVersion: inst.PostgresVersion, ToVersion: req.Target(), Step: domain.MajorUpgradeStepBackup,
		RequestedBy: actor, StartedAt: s.cfg.Now().UTC(),
	}
	if err := s.cfg.Runs.StartMajorUpgrade(ctx, run); err != nil {
		release()
		return nil, err
	}
	if err := s.setStatus(projectID, domain.StatusUpgrading, domain.MajorUpgradeStepBackup, ""); err != nil {
		s.endBeforeChange(context.WithoutCancel(ctx), run, err, "the upgrade could not be started")
		release()
		return nil, err
	}
	snapshot := *run
	drive := func() {
		defer release()
		stop := s.keepAlive(run.ID)
		defer stop()
		s.drive(context.Background(), engine, inst, run)
	}
	if s.runSync {
		drive()
	} else {
		go drive()
	}
	return &snapshot, nil
}

// admit refuses, before anything changes, a project the upgrade cannot take.
func (s *MajorUpgradeService) admit(ctx context.Context, inst *domain.DatabaseInstance, target string) (MajorUpgrader, error) {
	engine, reason := s.engineFor(inst)
	if engine == nil {
		return nil, fmt.Errorf("%w: %s", ErrMajorUpgradeUnsupported, reason)
	}
	if !config.IsPostgresUpgradeTarget(inst.PostgresVersion, target) {
		return nil, fmt.Errorf("%w (offered from %s: %s)", ErrMajorUpgradeTargetNotOffered, inst.PostgresVersion, offered(inst.PostgresVersion))
	}
	if !s.backupsReady(inst) {
		return nil, ErrMajorUpgradeNeedsBackups
	}
	blockers, err := engine.Preflight(ctx, inst, target)
	if err != nil {
		log.Printf("major upgrade %s: preflight: %v", inst.ProjectID, err)
		return nil, ErrMajorUpgradeCheckUnavailable
	}
	if len(blockers) > 0 {
		return nil, &MajorUpgradeBlockedError{Blockers: blockers}
	}
	return engine, nil
}

// backupsReady: the project backs up and its deployment has a store to write to.
func (s *MajorUpgradeService) backupsReady(inst *domain.DatabaseInstance) bool {
	return backupsOn(inst) && s.cfg.Backups != nil && s.cfg.Backups(inst)
}

func offered(current string) string {
	targets := config.PostgresUpgradeTargets(current)
	if len(targets) == 0 {
		return "none"
	}
	return strings.Join(targets, ", ")
}

// engineFor is the deployment's upgrader, or why there is none.
func (s *MajorUpgradeService) engineFor(inst *domain.DatabaseInstance) (MajorUpgrader, string) {
	switch {
	case inst.NoDatabase:
		return nil, "this project has no database"
	case inst.DBType == domain.MySQL:
		return nil, mysqlNotSupportedReason
	case inst.DBType != "" && inst.DBType != domain.PostgreSQL:
		return nil, "only Postgres databases can be upgraded here"
	case inst.DocumentDB:
		return nil, "a database with the DocumentDB extension cannot be upgraded yet: the extension has not been proven to survive a major upgrade"
	}
	mode := inst.DeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	engine := s.cfg.Engines[mode]
	if engine == nil {
		return nil, "major upgrades are not available on this installation"
	}
	return engine, ""
}

// mysqlNotSupportedReason: ADR 0041 offers MySQL 8.4 LTS only, and MOCO 0.37
// supports MySQL 8.0 and 8.4 only, so there is no newer major to move to.
const mysqlNotSupportedReason = "MySQL projects run MySQL 8.4 LTS, the newest version this platform supports; there is no newer major version to move to yet"

// drive runs a started upgrade to its end. Each path writes the record and
// the project's status before returning.
func (s *MajorUpgradeService) drive(ctx context.Context, engine MajorUpgrader, inst *domain.DatabaseInstance, run *domain.MajorUpgrade) {
	backupID, err := engine.TakeBackup(ctx, inst)
	if err != nil {
		s.endBeforeChange(ctx, run, err, "the backup taken before the upgrade did not complete")
		return
	}
	run.SafetyBackupID = backupID
	s.writeRun(ctx, run)
	if err := s.cfg.Projects.StopReplication(ctx, inst); err != nil {
		log.Printf("major upgrade %s: stop replication: %v", inst.ProjectID, err)
	}
	outcome := engine.Convert(ctx, inst, *run, &runProgress{svc: s, ctx: ctx, run: run})
	s.conclude(ctx, engine, inst, run, outcome)
}

// conclude finishes a run once its conversion ended, whichever way.
func (s *MajorUpgradeService) conclude(ctx context.Context, engine MajorUpgrader, inst *domain.DatabaseInstance, run *domain.MajorUpgrade, outcome UpgradeOutcome) {
	if outcome.Cause != nil {
		log.Printf("major upgrade %s %s: %v", inst.ProjectID, run.ID, outcome.Cause)
	}
	switch {
	case outcome.Unsettled:
		s.leaveOpen(ctx, run, outcome.Public)
	case outcome.Converted && run.Converted:
		s.finishConverted(ctx, engine, inst, run)
	case outcome.RolledBack:
		if err := s.cfg.Projects.RestartReplication(ctx, inst); err != nil {
			log.Printf("major upgrade %s: restart replication on %s: %v", inst.ProjectID, run.FromVersion, err)
		}
		s.end(ctx, run, domain.MajorUpgradeRolledBack, publicOr(outcome.Public, rolledBackSentence(run)), domain.StatusActive, "")
	default:
		s.endFailed(ctx, inst, run, outcome.Public)
	}
}

// leaveOpen keeps a run whose conversion has not settled RUNNING, and the
// project UPGRADING, for recovery to check again once this driver is gone.
func (s *MajorUpgradeService) leaveOpen(ctx context.Context, run *domain.MajorUpgrade, public string) {
	run.Step = domain.MajorUpgradeStepWaiting
	run.Error = public
	s.writeRun(ctx, run)
	if err := s.setStatus(run.ProjectID, domain.StatusUpgrading, domain.MajorUpgradeStepWaiting, public); err != nil {
		log.Printf("major upgrade %s: record waiting: %v", run.ProjectID, err)
	}
}

func rolledBackSentence(run *domain.MajorUpgrade) string {
	return fmt.Sprintf("the upgrade did not complete, so the database was put back on Postgres %s", run.FromVersion)
}

func publicOr(public, fallback string) string {
	if public != "" {
		return public
	}
	return fallback
}

// finishConverted makes the converted database whole: every copy serving,
// credentials as filed, a query answered, replication restarted, statistics
// and extensions refreshed, and a first backup on the new major.
func (s *MajorUpgradeService) finishConverted(ctx context.Context, engine MajorUpgrader, inst *domain.DatabaseInstance, run *domain.MajorUpgrade) {
	inst.PostgresVersion = run.ToVersion
	s.progressStep(ctx, run, domain.MajorUpgradeStepStarting)
	var warnings []string
	if err := engine.WaitServing(ctx, inst); err != nil {
		log.Printf("major upgrade %s: wait serving: %v", inst.ProjectID, err)
		warnings = append(warnings, "not every copy of the database was serving when the upgrade finished; replicas may still be copying data")
	}
	s.progressStep(ctx, run, domain.MajorUpgradeStepFinishing)
	if err := s.cfg.Projects.ReapplyCredentials(ctx, inst); err != nil {
		log.Printf("major upgrade %s: reapply credentials: %v", inst.ProjectID, err)
		warnings = append(warnings, "the project's passwords could not be set again; rotate the credentials if sign-ins fail")
	}
	if err := s.cfg.Probe.Probe(ctx, inst.ProjectID); err != nil {
		log.Printf("major upgrade %s: probe: %v", inst.ProjectID, err)
		warnings = append(warnings, "the upgraded database did not answer a test query")
	}
	if err := s.cfg.Projects.RestartReplication(ctx, inst); err != nil {
		log.Printf("major upgrade %s: restart replication: %v", inst.ProjectID, err)
		warnings = append(warnings, "realtime did not restart; changes are not streamed until it does")
	}
	s.cfg.Projects.AnnounceDatabaseReplaced(ctx, inst.ProjectID)
	warnings = append(warnings, engine.AfterSteps(ctx, inst)...)
	if !s.postUpgradeBackup(ctx, engine, inst) {
		warnings = append(warnings, fmt.Sprintf("there is no backup on Postgres %s yet; take one from Backups", run.ToVersion))
	}
	engine.Cleanup(ctx, inst, *run)
	run.Warnings = warnings
	s.end(ctx, run, domain.MajorUpgradeCompleted, "", domain.StatusActive, "")
}

func (s *MajorUpgradeService) postUpgradeBackup(ctx context.Context, engine MajorUpgrader, inst *domain.DatabaseInstance) bool {
	for attempt := 1; attempt <= postUpgradeBackupAttempts; attempt++ {
		_, err := engine.TakeBackup(ctx, inst)
		if err == nil {
			return true
		}
		log.Printf("major upgrade %s: backup on the new major, attempt %d: %v", inst.ProjectID, attempt, err)
	}
	return false
}

// endBeforeChange ends a run that changed nothing; what says why, for the user.
func (s *MajorUpgradeService) endBeforeChange(ctx context.Context, run *domain.MajorUpgrade, cause error, what string) {
	log.Printf("major upgrade %s %s: before any change: %v", run.ProjectID, run.ID, cause)
	public := fmt.Sprintf("%s, so nothing was changed; the database is still on Postgres %s", what, run.FromVersion)
	s.end(ctx, run, domain.MajorUpgradeRolledBack, public, domain.StatusActive, "")
}

// endFailed ends a run the deployment could not put back. The project stays
// usable if its database answers; either way the user is told which backup
// holds the data from just before.
func (s *MajorUpgradeService) endFailed(ctx context.Context, inst *domain.DatabaseInstance, run *domain.MajorUpgrade, public string) {
	advice := fmt.Sprintf("%s. Its data from just before the upgrade is in backup %s; restore that backup into a new project if the database does not come back",
		publicOr(public, "the upgrade stopped before it finished"), run.SafetyBackupID)
	status, reason := domain.StatusActive, ""
	if err := s.cfg.Probe.Probe(ctx, inst.ProjectID); err != nil {
		status, reason = domain.StageFailed, advice
	}
	s.end(ctx, run, domain.MajorUpgradeFailed, advice, status, reason)
}

// end writes the run's outcome, then the project's status.
func (s *MajorUpgradeService) end(ctx context.Context, run *domain.MajorUpgrade, outcome, public string, status domain.ProvisioningStage, reason string) {
	finished := s.cfg.Now().UTC()
	run.Status = outcome
	run.Error = public
	run.FinishedAt = &finished
	s.writeRun(ctx, run)
	if err := s.setStatus(run.ProjectID, status, "", reason); err != nil {
		log.Printf("major upgrade %s: record status %s: %v", run.ProjectID, status, err)
	}
}

func (s *MajorUpgradeService) writeRun(ctx context.Context, run *domain.MajorUpgrade) {
	if err := s.cfg.Runs.UpdateMajorUpgrade(ctx, run); err != nil {
		log.Printf("major upgrade %s: write run %s: %v", run.ProjectID, run.ID, err)
	}
}

func (s *MajorUpgradeService) progressStep(ctx context.Context, run *domain.MajorUpgrade, step string) {
	run.Step = step
	s.writeRun(ctx, run)
	if err := s.setStatus(run.ProjectID, domain.StatusUpgrading, step, ""); err != nil {
		log.Printf("major upgrade %s: record step %s: %v", run.ProjectID, step, err)
	}
}

// setStatus writes the project's status from its current row and tells the observers.
func (s *MajorUpgradeService) setStatus(projectID string, status domain.ProvisioningStage, step, reason string) error {
	row, err := s.cfg.Instances.FindByProjectID(projectID)
	if err != nil || row == nil {
		return fmt.Errorf("read project %s: %w", projectID, errors.Join(err, storage.ErrProjectNotFound))
	}
	row.Status = string(status)
	row.CurrentStep = step
	row.FailureReason = reason
	if err := s.cfg.Instances.Update(row); err != nil {
		return fmt.Errorf("record project %s %s: %w", projectID, status, err)
	}
	for _, observer := range s.cfg.Observers {
		observer.ProjectStatusChanged(projectID, string(status))
	}
	return nil
}

// runProgress persists what a conversion reports as it happens.
type runProgress struct {
	svc *MajorUpgradeService
	ctx context.Context
	run *domain.MajorUpgrade
}

func (p *runProgress) Step(step string) { p.svc.progressStep(p.ctx, p.run, step) }

func (p *runProgress) WritesStopped(at time.Time) {
	stopped := at.UTC()
	p.run.WritesStoppedAt = &stopped
	p.svc.writeRun(p.ctx, p.run)
}

func (p *runProgress) Converted(at time.Time) error {
	if err := p.svc.cfg.Runs.MarkMajorUpgradeConverted(p.ctx, p.run.ID, p.run.ProjectID, p.run.ToVersion, at); err != nil {
		return fmt.Errorf("record the conversion to %s: %w", p.run.ToVersion, err)
	}
	converted := at.UTC()
	p.run.Converted = true
	p.run.ConvertedAt = &converted
	return nil
}

// RecoverInterrupted ends every run whose driver died: a run whose project
// lease is free has nobody driving it. A converted run is only finished; any
// other is handed to its deployment to finish or put back.
func (s *MajorUpgradeService) RecoverInterrupted(ctx context.Context) {
	runs, err := s.cfg.Runs.RunningMajorUpgrades(ctx)
	if err != nil {
		log.Printf("major upgrade recovery: list running: %v", err)
		return
	}
	for i := range runs {
		s.recoverOne(ctx, runs[i])
	}
}

func (s *MajorUpgradeService) recoverOne(ctx context.Context, run domain.MajorUpgrade) {
	if s.cfg.Now().Sub(run.HeartbeatAt) < majorUpgradeStaleAfter {
		return // its driver is alive, even if its lease was lost
	}
	inst, release, err := s.cfg.Projects.HoldForMajorUpgrade(ctx, run.ProjectID, admitRecovery)
	if err != nil {
		if !errors.Is(err, ErrProjectOperationRunning) {
			log.Printf("major upgrade recovery %s: %v", run.ProjectID, err)
		}
		return
	}
	defer release()
	stop := s.keepAlive(run.ID)
	defer stop()
	engine, _ := s.engineFor(inst)
	if engine == nil {
		s.endFailed(ctx, inst, &run, "the upgrade was interrupted")
		return
	}
	if run.Converted {
		s.finishConverted(ctx, engine, inst, &run)
		return
	}
	outcome := engine.Recover(ctx, inst, run, &runProgress{svc: s, ctx: ctx, run: &run})
	s.conclude(ctx, engine, inst, &run, outcome)
}

// majorUpgradeHeartbeat is how often a driver marks its run alive;
// majorUpgradeStaleAfter is how long without it before recovery takes over.
const (
	majorUpgradeHeartbeat  = 30 * time.Second
	majorUpgradeStaleAfter = 3 * time.Minute
)

// keepAlive marks the run alive until the returned stop is called.
func (s *MajorUpgradeService) keepAlive(runID string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(majorUpgradeHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.cfg.Runs.TouchMajorUpgrade(ctx, runID, s.cfg.Now()); err != nil && ctx.Err() == nil {
					log.Printf("major upgrade %s: heartbeat: %v", runID, err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// admitRecovery lets recovery take any project not being torn down.
func admitRecovery(status string) error {
	if domain.IsDeletionStatus(status) || status == string(domain.StatusPendingDeletion) {
		return fmt.Errorf("project is %s", status)
	}
	return nil
}

// StartRecoverySweeper recovers interrupted runs now and every interval, as
// leader only. It returns the stop.
func (s *MajorUpgradeService) StartRecoverySweeper(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			if isLeader(ctx, leader) {
				s.RecoverInterrupted(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

func isLeader(ctx context.Context, leader LeaderChecker) bool {
	if leader == nil {
		return true
	}
	ok, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("major upgrade recovery: leadership: %v", err)
	}
	return ok && err == nil
}
