package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A RUNNING major upgrade belongs to one actor at a time: the driver that
// started it, or the recovery that claimed it once the driver's heartbeat
// went stale. Every write names the owner, so a write by an actor that lost
// the run is refused, and that actor stops where it is.

// majorUpgradeHeartbeat is how often an actor marks its run alive, and
// majorUpgradeHeartbeatTimeout bounds one mark. majorUpgradeStaleAfter is
// how long without a mark before recovery may claim the run; an actor whose
// marks have failed for majorUpgradeFenceAfter gives the run up first. The
// check runs on a tick, so giving up can be a heartbeat late, and a step in
// flight then still finishes: the minute and the heartbeat left cover both.
const (
	majorUpgradeHeartbeat        = 30 * time.Second
	majorUpgradeHeartbeatTimeout = 10 * time.Second
	majorUpgradeStaleAfter       = 3 * time.Minute
	majorUpgradeFenceAfter       = majorUpgradeStaleAfter - time.Minute - majorUpgradeHeartbeat
)

// newOwner names one actor on one run.
func newOwner() string {
	return "actor-" + newMajorUpgradeID()
}

// runProgress persists what a conversion reports as it happens, as the
// run's owner.
type runProgress struct {
	svc *MajorUpgradeService
	run *domain.MajorUpgrade
}

func (s *MajorUpgradeService) progressOf(run *domain.MajorUpgrade) *runProgress {
	return &runProgress{svc: s, run: run}
}

func (p *runProgress) Step(ctx context.Context, step string) error {
	return p.svc.progressStep(ctx, p.run, step)
}

func (p *runProgress) WritesStopped(ctx context.Context, at time.Time) error {
	stopped := at.UTC()
	p.run.WritesStoppedAt = &stopped
	return p.svc.writeRun(ctx, p.run)
}

// Hold confirms ownership with a heartbeat; it answers even after the
// actor's context was cancelled, so a lost run is told apart from a stop,
// and within heartbeatTimeout.
func (p *runProgress) Hold(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.svc.heartbeatTimeout)
	defer cancel()
	return takenOver(p.svc.cfg.Runs.TouchMajorUpgrade(ctx, p.run.ID, p.run.Owner))
}

func (p *runProgress) Converted(ctx context.Context, at time.Time) error {
	err := p.svc.cfg.Runs.MarkMajorUpgradeConverted(ctx, p.run.ID, p.run.ProjectID, p.run.ToVersion, p.run.Owner, at)
	if err != nil {
		landed, readErr := p.markLanded(ctx)
		if readErr != nil {
			return fmt.Errorf("record the conversion to %s: %w", p.run.ToVersion, errors.Join(takenOver(err), readErr))
		}
		at = landed
	}
	converted := at.UTC()
	p.run.Converted = true
	p.run.ConvertedAt = &converted
	return nil
}

// markLanded reads back a mark that answered an error: it counts only when
// this actor still owns the RUNNING run and the mark committed.
func (p *runProgress) markLanded(ctx context.Context) (time.Time, error) {
	stored, err := p.svc.cfg.Runs.LatestMajorUpgrade(ctx, p.run.ProjectID)
	switch {
	case err != nil:
		return time.Time{}, err
	case stored == nil || stored.ID != p.run.ID:
		return time.Time{}, errors.New("the run is gone")
	case stored.Status != domain.MajorUpgradeRunning || stored.Owner != p.run.Owner:
		return time.Time{}, errRunTakenOver
	case !stored.Converted || stored.ConvertedAt == nil:
		return time.Time{}, errors.New("the mark did not land")
	}
	return *stored.ConvertedAt, nil
}

// stillOwns reports whether the actor may act on outcome. The read is tried
// twice; an outcome it cannot act on is logged and dropped.
func (s *MajorUpgradeService) stillOwns(ctx context.Context, run *domain.MajorUpgrade, outcome UpgradeOutcome) bool {
	if errors.Is(outcome.Cause, errRunTakenOver) {
		log.Printf("major upgrade %s %s: the run was taken over; dropping %s", run.ProjectID, run.ID, describe(outcome))
		return false
	}
	owned, err := s.owns(ctx, run)
	if err != nil {
		owned, err = s.owns(ctx, run)
	}
	if err != nil || !owned {
		log.Printf("major upgrade %s %s: not this actor's run (%v); dropping %s", run.ProjectID, run.ID, err, describe(outcome))
		return false
	}
	return true
}

func (s *MajorUpgradeService) owns(ctx context.Context, run *domain.MajorUpgrade) (bool, error) {
	stored, err := s.cfg.Runs.LatestMajorUpgrade(ctx, run.ProjectID)
	if err != nil {
		return false, err
	}
	return stored != nil && stored.ID == run.ID && stored.Status == domain.MajorUpgradeRunning && stored.Owner == run.Owner, nil
}

func describe(outcome UpgradeOutcome) string {
	return fmt.Sprintf("outcome converted=%v rolledBack=%v unsettled=%v %q", outcome.Converted, outcome.RolledBack, outcome.Unsettled, outcome.Public)
}

// RecoverInterrupted takes over every run whose actor stopped marking it
// alive. A claimed converted run is only finished; any other is handed to
// its deployment to finish or put back.
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

// recoverOne works only from the run as its claim returned it, and the
// project as read after the claim; the listed copy may be out of date.
// Whether the run is stale is the claim's call, on the database's clock.
func (s *MajorUpgradeService) recoverOne(ctx context.Context, listed domain.MajorUpgrade) {
	// Read before the lease: the deletion sweep gives up on a project it finds held.
	if inst, err := s.cfg.Instances.FindByProjectID(listed.ProjectID); err == nil && inst != nil && admitRecovery(inst.Status) != nil {
		return
	}
	_, release, err := s.cfg.Projects.HoldForMajorUpgrade(ctx, listed.ProjectID, admitRecovery)
	if err != nil {
		if !errors.Is(err, ErrProjectOperationRunning) && !errors.Is(err, errRecoveryNotOwed) {
			log.Printf("major upgrade recovery %s: %v", listed.ProjectID, err)
		}
		return
	}
	defer release()
	stamped := s.cfg.Now()
	run, err := s.cfg.Runs.ClaimMajorUpgrade(ctx, listed.ID, newOwner(), majorUpgradeStaleAfter)
	if err != nil || run == nil {
		if err != nil {
			log.Printf("major upgrade recovery %s: claim %s: %v", listed.ProjectID, listed.ID, err)
		}
		return
	}
	// The heartbeat runs from the claim's stamp, before anything else.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := s.keepAlive(*run, stamped, cancel)
	defer stop()
	inst, err := s.cfg.Instances.FindByProjectID(run.ProjectID)
	if err != nil || inst == nil {
		log.Printf("major upgrade recovery %s: read project after the claim: %v", run.ProjectID, err)
		return
	}
	s.recoverClaimed(ctx, inst, run)
}

func (s *MajorUpgradeService) recoverClaimed(ctx context.Context, inst *domain.DatabaseInstance, run *domain.MajorUpgrade) {
	engine, _ := s.engineFor(inst)
	switch {
	case engine == nil:
		s.endFailed(ctx, inst, run, "the upgrade was interrupted")
	case run.Converted:
		s.finishConverted(ctx, engine, inst, run)
	default:
		outcome := engine.Recover(ctx, inst, *run, s.progressOf(run))
		s.conclude(ctx, engine, inst, run, outcome)
	}
}

// keepAlive marks the run alive until the returned stop is called. The
// actor is cancelled with errRunTakenOver when a mark is refused because the
// run is no longer its own, or when no mark has landed for
// majorUpgradeFenceAfter since stamped, the time taken just before the run's
// heartbeat was last written: by then recovery may be about to claim it.
func (s *MajorUpgradeService) keepAlive(run domain.MajorUpgrade, stamped time.Time, lost func(error)) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.heartbeatEvery)
		defer ticker.Stop()
		lastBeat := stamped
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				if s.fenced(run, &lastBeat, lost) {
					return
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// fenced marks the run alive once and reports whether the actor gave it up.
// A mark counts from when it was sent, the earliest it could have landed.
func (s *MajorUpgradeService) fenced(run domain.MajorUpgrade, lastBeat *time.Time, lost func(error)) bool {
	sent := s.cfg.Now()
	err := s.beat(run)
	now := s.cfg.Now()
	if err == nil {
		*lastBeat = sent
		return false
	}
	log.Printf("major upgrade %s: heartbeat: %v", run.ID, err)
	if errors.Is(takenOver(err), errRunTakenOver) {
		lost(errRunTakenOver)
		return true
	}
	if now.Sub(*lastBeat) >= majorUpgradeFenceAfter {
		lost(fmt.Errorf("%w: no heartbeat landed for %s", errRunTakenOver, now.Sub(*lastBeat)))
		return true
	}
	return false
}

// beat marks the run alive once, within heartbeatTimeout.
func (s *MajorUpgradeService) beat(run domain.MajorUpgrade) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.heartbeatTimeout)
	defer cancel()
	return s.cfg.Runs.TouchMajorUpgrade(ctx, run.ID, run.Owner)
}

// fence carries a lost run to the actor's context, which may be made after
// the heartbeat started.
type fence struct {
	mu     sync.Mutex
	cause  error
	cancel context.CancelCauseFunc
}

func (f *fence) lose(cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cause = cause
	if f.cancel != nil {
		f.cancel(cause)
	}
}

// attach cancels with the cause already met, or any met later.
func (f *fence) attach(cancel context.CancelCauseFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel = cancel
	if f.cause != nil {
		cancel(f.cause)
	}
}

// fenceKey holds the context a detached one came from.
type fenceKey struct{}

// detachFence keeps ctx's values but not its cancellation, so a call in
// flight is never cut off; runLostBy still sees a fence met on ctx.
func detachFence(ctx context.Context) context.Context {
	return context.WithValue(context.WithoutCancel(ctx), fenceKey{}, ctx)
}

// runLostBy is the cause when ctx, or the context it was detached from, was
// cancelled because the run is no longer the actor's; checked between steps.
func runLostBy(ctx context.Context) error {
	if parent, ok := ctx.Value(fenceKey{}).(context.Context); ok {
		ctx = parent
	}
	if cause := context.Cause(ctx); errors.Is(cause, errRunTakenOver) {
		return cause
	}
	return nil
}

// admitRecovery lets recovery take any project not being torn down.
var errRecoveryNotOwed = errors.New("recovery is not owed")

func admitRecovery(status string) error {
	if domain.IsDeletionStatus(status) || status == string(domain.StatusPendingDeletion) {
		return fmt.Errorf("%w: project is %s", errRecoveryNotOwed, status)
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
		log.Printf("leadership check: %v", err)
	}
	return ok && err == nil
}
