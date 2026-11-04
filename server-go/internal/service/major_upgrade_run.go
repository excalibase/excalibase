package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A RUNNING major upgrade belongs to one actor at a time: the driver that
// started it, or the recovery that claimed it once the driver's heartbeat
// went stale. Every write names the owner, so a write by an actor that lost
// the run is refused, and that actor stops where it is.

// majorUpgradeHeartbeat is how often an actor marks its run alive;
// majorUpgradeStaleAfter is how long without it before recovery takes over.
const (
	majorUpgradeHeartbeat  = 30 * time.Second
	majorUpgradeStaleAfter = 3 * time.Minute
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
// actor's context was cancelled, so a lost run is told apart from a stop.
func (p *runProgress) Hold(ctx context.Context) error {
	err := p.svc.cfg.Runs.TouchMajorUpgrade(context.WithoutCancel(ctx), p.run.ID, p.run.Owner, p.svc.cfg.Now())
	return takenOver(err)
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
func (s *MajorUpgradeService) recoverOne(ctx context.Context, listed domain.MajorUpgrade) {
	if s.cfg.Now().Sub(listed.HeartbeatAt) < majorUpgradeStaleAfter {
		return // its actor is alive, even if its lease was lost
	}
	_, release, err := s.cfg.Projects.HoldForMajorUpgrade(ctx, listed.ProjectID, admitRecovery)
	if err != nil {
		if !errors.Is(err, ErrProjectOperationRunning) {
			log.Printf("major upgrade recovery %s: %v", listed.ProjectID, err)
		}
		return
	}
	defer release()
	run, inst := s.claim(ctx, listed)
	if run == nil {
		return
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := s.keepAlive(*run, cancel)
	defer stop()
	s.recoverClaimed(ctx, inst, run)
}

// claim takes the run over and reads the project afresh; nil when the run
// is not claimable any more.
func (s *MajorUpgradeService) claim(ctx context.Context, listed domain.MajorUpgrade) (*domain.MajorUpgrade, *domain.DatabaseInstance) {
	now := s.cfg.Now()
	run, err := s.cfg.Runs.ClaimMajorUpgrade(ctx, listed.ID, newOwner(), now.Add(-majorUpgradeStaleAfter), now)
	if err != nil || run == nil {
		if err != nil {
			log.Printf("major upgrade recovery %s: claim %s: %v", listed.ProjectID, listed.ID, err)
		}
		return nil, nil
	}
	inst, err := s.cfg.Instances.FindByProjectID(run.ProjectID)
	if err != nil || inst == nil {
		log.Printf("major upgrade recovery %s: read project after the claim: %v", run.ProjectID, err)
		return nil, nil
	}
	return run, inst
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

// keepAlive marks the run alive until the returned stop is called. A
// heartbeat refused because the run is no longer this actor's cancels the
// actor with errRunTakenOver.
func (s *MajorUpgradeService) keepAlive(run domain.MajorUpgrade, lost context.CancelCauseFunc) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.heartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				if s.beat(run, lost) {
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

// beat marks the run alive once; true when the run was lost.
func (s *MajorUpgradeService) beat(run domain.MajorUpgrade, lost context.CancelCauseFunc) bool {
	err := s.cfg.Runs.TouchMajorUpgrade(context.Background(), run.ID, run.Owner, s.cfg.Now())
	if err == nil {
		return false
	}
	log.Printf("major upgrade %s: heartbeat: %v", run.ID, err)
	if errors.Is(takenOver(err), errRunTakenOver) {
		lost(errRunTakenOver)
		return true
	}
	return false
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
