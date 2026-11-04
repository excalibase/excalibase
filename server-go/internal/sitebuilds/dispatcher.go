package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const (
	lostBuildReason  = "the build controller stopped while this build ran; push again or rebuild"
	stuckBuildReason = "the cluster did not finish the build in time; build again"
)

// DispatcherOptions pace the dispatcher; zero values take the defaults.
type DispatcherOptions struct {
	// Slots is how many builds run at once across the install.
	Slots int
	// Holder names this replica on the builds it runs.
	Holder string
	// SpoolDir holds a build's packed output while it deploys; "" is the system temp dir.
	SpoolDir    string
	Poll        time.Duration
	StatusPoll  time.Duration
	LogEvery    time.Duration
	Lease       time.Duration
	DeployRetry time.Duration
	DeployTries int
	// StartTimeout bounds creating the Job; a stuck API server fails the build, not the dispatcher.
	StartTimeout time.Duration
	// PendingGrace is how long past the plan's time limit a build may go unfinished (image pull, scheduling).
	PendingGrace time.Duration
	// DeployBudget bounds collecting the output and deploying it, retries included.
	DeployBudget time.Duration
	// ShutdownGrace is how long a stopping dispatcher lets running builds finish before handing them back to the queue.
	ShutdownGrace time.Duration
}

func (o DispatcherOptions) withDefaults() DispatcherOptions {
	defaults := DispatcherOptions{Slots: 1, Poll: 5 * time.Second, StatusPoll: 3 * time.Second, LogEvery: 15 * time.Second,
		Lease: 90 * time.Second, DeployRetry: 15 * time.Second, DeployTries: 8, StartTimeout: time.Minute,
		PendingGrace: 10 * time.Minute, DeployBudget: 20 * time.Minute, ShutdownGrace: 10 * time.Second}
	if o.Slots <= 0 {
		o.Slots = defaults.Slots
	}
	if o.Holder == "" {
		host, _ := os.Hostname()
		o.Holder = fmt.Sprintf("%s-%d", host, time.Now().UnixNano())
	}
	for _, pair := range []struct{ value, fallback *time.Duration }{
		{&o.Poll, &defaults.Poll}, {&o.StatusPoll, &defaults.StatusPoll}, {&o.LogEvery, &defaults.LogEvery},
		{&o.Lease, &defaults.Lease}, {&o.DeployRetry, &defaults.DeployRetry}, {&o.StartTimeout, &defaults.StartTimeout},
		{&o.PendingGrace, &defaults.PendingGrace}, {&o.DeployBudget, &defaults.DeployBudget}, {&o.ShutdownGrace, &defaults.ShutdownGrace},
	} {
		if *pair.value <= 0 {
			*pair.value = *pair.fallback
		}
	}
	if o.DeployTries <= 0 {
		o.DeployTries = defaults.DeployTries
	}
	return o
}

// Dispatcher runs queued builds of sites and apps. Every replica runs one; the store's claim keeps the caps across them.
type Dispatcher struct {
	service *Service
	runner  Runner
	// apps is nil until app builds are wired; the flag still decides whether they are claimed.
	apps    *appDispatch
	options DispatcherOptions
	wake    chan struct{}
	running sync.WaitGroup
	// interrupted closes once the shutdown grace is spent; running builds then go back to the queue.
	interrupted   chan struct{}
	interruptOnce sync.Once
	// stopped closes on Shutdown, for a Run with no context of its own to end.
	stopped  chan struct{}
	stopOnce sync.Once
}

func NewDispatcher(service *Service, runner Runner, options DispatcherOptions) *Dispatcher {
	dispatcher := &Dispatcher{service: service, runner: runner, options: options.withDefaults(), wake: make(chan struct{}, 1),
		interrupted: make(chan struct{}), stopped: make(chan struct{})}
	service.nudge = dispatcher.Nudge
	return dispatcher
}

// appDispatch is what runs and publishes app builds.
type appDispatch struct {
	runner    AppRunner
	publisher AppPublisher
}

// EnableApps lets the dispatcher run app builds; with no publisher, each fails before it runs. Call it before Run.
func (d *Dispatcher) EnableApps(runner AppRunner, publisher AppPublisher) {
	d.apps = &appDispatch{runner: runner, publisher: publisher}
}

// claimKinds are the kinds this dispatcher runs now: app builds only while wired and switched on.
func (d *Dispatcher) claimKinds(ctx context.Context) []string {
	if d.apps != nil && d.service.appsOn(ctx) {
		return []string{KindSite, KindApp}
	}
	return []string{KindSite}
}

const appsSwitchedOffReason = "git builds of apps were switched off on this install before this build started; nothing was built"

// cancelSwitchedOff cancels queued app builds while the appgitbuilds flag is off, so none waits for it by surprise.
func (d *Dispatcher) cancelSwitchedOff(ctx context.Context) {
	if d.service.apps == nil || d.service.appsOn(ctx) {
		return
	}
	if _, err := d.service.store.CancelQueued(ctx, KindApp, appsSwitchedOffReason); err != nil {
		log.Printf("git builds: cancel queued app builds: %v", err)
	}
}

func (d *Dispatcher) jobsFor(kind string) JobControl {
	if kind == KindApp && d.apps != nil {
		return d.apps.runner
	}
	if kind == KindApp {
		return nil
	}
	return d.runner
}

// Nudge wakes the dispatcher now instead of at its next poll.
func (d *Dispatcher) Nudge() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Shutdown ends Run as its context ending would.
func (d *Dispatcher) Shutdown() {
	d.stopOnce.Do(func() { close(d.stopped) })
}

// Run claims and starts builds until ctx ends or Shutdown; builds still running
// after the shutdown grace are interrupted and handed back to the queue.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.options.Poll)
	defer ticker.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			d.stop()
			return
		case <-d.stopped:
			d.stop()
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

// Tick fails builds whose replica went away, then starts every build the caps allow.
func (d *Dispatcher) Tick(ctx context.Context) {
	d.expireLost(ctx)
	d.cancelSwitchedOff(ctx)
	kinds := d.claimKinds(ctx)
	for ctx.Err() == nil {
		build, err := d.service.store.ClaimNext(ctx, d.options.Slots, d.options.Holder, d.service.now().Add(d.options.Lease), kinds)
		if err != nil {
			log.Printf("git builds: claim: %v", err)
			return
		}
		if build == nil {
			return
		}
		d.running.Add(1)
		go d.runInterruptible(build)
	}
}

// Wait blocks until every build this dispatcher started has finished.
func (d *Dispatcher) Wait() { d.running.Wait() }

func (d *Dispatcher) stop() {
	finished := make(chan struct{})
	go func() {
		d.running.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return
	case <-time.After(d.options.ShutdownGrace):
	}
	d.interruptOnce.Do(func() { close(d.interrupted) })
	<-finished
}

// runInterruptible runs a claimed build on its own context, which only a spent shutdown grace ends.
func (d *Dispatcher) runInterruptible(build *Build) {
	defer d.running.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-d.interrupted:
			cancel()
		case <-ctx.Done():
		}
	}()
	d.run(ctx, build)
}

func (d *Dispatcher) expireLost(ctx context.Context) {
	lost, err := d.service.store.ExpireLostBuilds(ctx, d.service.now(), lostBuildReason)
	if err != nil {
		log.Printf("git builds: expire lost builds: %v", err)
		return
	}
	for _, build := range lost {
		jobs := d.jobsFor(build.Kind)
		if jobs == nil {
			log.Printf("%s build %s: lost, and no runner here removes its job", build.Kind, build.ID)
			continue
		}
		for attempt := range maxAttempts {
			if err := jobs.Remove(ctx, runID(build.ID, attempt)); err != nil {
				log.Printf("%s build %s: remove the lost build's job: %v", build.Kind, build.ID, err)
			}
		}
	}
}

// run holds the build's lease while it runs and always records an outcome.
func (d *Dispatcher) run(ctx context.Context, build *Build) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leaseLost := make(chan struct{})
	go d.holdLease(runCtx, cancel, build.ID, leaseLost)
	execution := newExecution(d, build)
	outcome := execution.execute(runCtx)
	cancel()
	select {
	case <-leaseLost:
		log.Printf("%s build %s: lease lost; another replica recorded it", build.Kind, build.ID)
		return
	default:
	}
	if ctx.Err() != nil {
		if err := d.service.store.RequeueBuild(context.WithoutCancel(ctx), build.ID, d.options.Holder); err != nil {
			log.Printf("%s build %s: hand back to the queue: %v", build.Kind, build.ID, err)
		}
		return
	}
	build.Status, build.DeployID, build.FailureKind, build.FailureReason = outcome.status, outcome.deployID, outcome.kind, outcome.reason
	build.Log = redact(execution.fullLog(), execution.secrets)
	if err := d.service.store.FinishBuild(context.WithoutCancel(ctx), build, d.options.Holder); err != nil {
		log.Printf("%s build %s: record outcome: %v", build.Kind, build.ID, err)
	}
}

func (d *Dispatcher) holdLease(ctx context.Context, cancel context.CancelFunc, buildID string, lost chan<- struct{}) {
	ticker := time.NewTicker(d.options.Lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := d.service.store.ExtendLease(ctx, buildID, d.options.Holder, d.service.now().Add(d.options.Lease))
			if errors.Is(err, ErrLeaseLost) {
				close(lost)
				cancel()
				return
			}
			if err != nil {
				log.Printf("git build %s: extend lease: %v", buildID, err)
			}
		}
	}
}

type outcome struct {
	status   string
	deployID string
	kind     FailureKind
	reason   string
}

func failedWith(kind FailureKind, reason string) outcome {
	return outcome{status: StatusFailed, kind: kind, reason: reason}
}

// refuse is a build stopped before or instead of running.
func refuse(kind FailureKind, reason string) *outcome {
	stopped := failedWith(kind, reason)
	return &stopped
}

// refusal words a step's error: what the git host no longer allows is the clone's failure, anything else the platform's.
func refusal(err error) *outcome {
	var refused *InputError
	if errors.As(err, &refused) {
		return refuse(FailClone, refused.Reason)
	}
	return refuse(FailPlatform, userMessage(err))
}
