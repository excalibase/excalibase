package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
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

// Dispatcher runs queued builds. Every replica runs one; the store's claim keeps the caps across them.
type Dispatcher struct {
	service *Service
	runner  Runner
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
	for ctx.Err() == nil {
		build, err := d.service.store.ClaimNext(ctx, d.options.Slots, d.options.Holder, d.service.now().Add(d.options.Lease))
		if err != nil {
			log.Printf("site builds: claim: %v", err)
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
	ids, err := d.service.store.ExpireLostBuilds(ctx, d.service.now(), lostBuildReason)
	if err != nil {
		log.Printf("site builds: expire lost builds: %v", err)
		return
	}
	for _, id := range ids {
		if err := d.runner.Remove(ctx, id); err != nil {
			log.Printf("site build %s: remove the lost build's job: %v", id, err)
		}
	}
}

// run holds the build's lease while it runs and always records an outcome.
func (d *Dispatcher) run(ctx context.Context, build *Build) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leaseLost := make(chan struct{})
	go d.holdLease(runCtx, cancel, build.ID, leaseLost)
	execution := &execution{dispatcher: d, build: build, secrets: map[string]string{}}
	outcome := execution.execute(runCtx)
	cancel()
	select {
	case <-leaseLost:
		log.Printf("site build %s: lease lost; another replica recorded it", build.ID)
		return
	default:
	}
	if ctx.Err() != nil {
		if err := d.service.store.RequeueBuild(context.WithoutCancel(ctx), build.ID, d.options.Holder); err != nil {
			log.Printf("site build %s: hand back to the queue: %v", build.ID, err)
		}
		return
	}
	build.Status, build.DeployID, build.FailureKind, build.FailureReason = outcome.status, outcome.deployID, outcome.kind, outcome.reason
	build.Log = redact(execution.log, execution.secrets)
	if err := d.service.store.FinishBuild(context.WithoutCancel(ctx), build, d.options.Holder); err != nil {
		log.Printf("site build %s: record outcome: %v", build.ID, err)
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
				log.Printf("site build %s: extend lease: %v", buildID, err)
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

// execution is one build's run.
type execution struct {
	dispatcher *Dispatcher
	build      *Build
	secrets    map[string]string
	log        string
}

func (e *execution) execute(ctx context.Context) outcome {
	service, build := e.dispatcher.service, e.build
	connection, err := service.store.GetConnection(ctx, build.ProjectID, build.SiteID)
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	if connection == nil || connection.RepositoryID != build.RepositoryID {
		return failedWith(FailPlatform, "the repository was disconnected before this build started")
	}
	if err := service.checkConnector(ctx, connection); err != nil {
		return failedWith(FailClone, userMessage(err))
	}
	if err := service.currentRepository(ctx, connection); err != nil {
		var refused *InputError
		if errors.As(err, &refused) {
			return failedWith(FailClone, refused.Reason)
		}
		return failedWith(FailPlatform, userMessage(err))
	}
	build.Repository = connection.Repository
	spec, revoke, err := e.prepare(ctx)
	if err != nil {
		var refused *InputError
		if errors.As(err, &refused) {
			return failedWith(FailClone, refused.Reason)
		}
		return failedWith(FailPlatform, userMessage(err))
	}
	defer revoke()
	startCtx, cancelStart := context.WithTimeout(ctx, e.dispatcher.options.StartTimeout)
	err = e.dispatcher.runner.Start(startCtx, spec)
	cancelStart()
	defer e.remove(ctx)
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	awaitCtx, cancelAwait := context.WithTimeout(ctx, spec.Timeout+e.dispatcher.options.PendingGrace)
	status := e.await(awaitCtx, revoke)
	cancelAwait()
	revoke()
	if status.Phase != RunReady {
		return failedWith(status.Failure, failureMessage(status, build.Limits, build.Settings))
	}
	if reason := e.stillDeployable(ctx); reason != "" {
		return failedWith(FailPlatform, reason)
	}
	deployCtx, cancelDeploy := context.WithTimeout(ctx, e.dispatcher.options.DeployBudget)
	defer cancelDeploy()
	return e.deploy(deployCtx)
}

// stillDeployable re-reads the connection and its connector just before the deploy; it answers why not, or "".
func (e *execution) stillDeployable(ctx context.Context) string {
	service, build := e.dispatcher.service, e.build
	still, err := service.store.GetConnection(ctx, build.ProjectID, build.SiteID)
	if err != nil {
		return userMessage(err)
	}
	if still == nil || still.RepositoryID != build.RepositoryID {
		return "the repository was disconnected while this build ran; nothing was deployed"
	}
	if err := service.checkConnector(ctx, still); err != nil {
		return userMessage(err) + "; nothing was deployed"
	}
	return ""
}

// noRevoke stands in when no clone credential was minted.
func noRevoke() {
	// nothing was minted, so nothing goes back
}

// prepare mints the clone credential and gathers the build's variables.
func (e *execution) prepare(ctx context.Context) (RunSpec, func(), error) {
	service, build := e.dispatcher.service, e.build
	credential, err := service.provider.CloneCredential(ctx, build.InstallationID, build.RepositoryID)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return RunSpec{}, noRevoke, &InputError{Reason: "the GitHub App no longer covers " + build.Repository}
	}
	if err != nil {
		return RunSpec{}, noRevoke, fmt.Errorf("mint a clone credential: %w", err)
	}
	var revokeOnce sync.Once
	revoke := func() {
		revokeOnce.Do(func() {
			if err := service.provider.RevokeCloneCredential(context.WithoutCancel(ctx), credential.Token); err != nil {
				log.Printf("site build %s: revoke the clone credential: %v", build.ID, err)
			}
		})
	}
	env, err := e.environment(ctx)
	if err != nil {
		revoke()
		return RunSpec{}, noRevoke, err
	}
	largest, err := service.limits.LargestBuild(ctx)
	if err != nil {
		revoke()
		return RunSpec{}, noRevoke, fmt.Errorf("size the build quota: %w", err)
	}
	repository := gitprovider.Repository{ID: build.RepositoryID, FullName: build.Repository}
	settings, limits := build.Settings, build.Limits
	return RunSpec{
		BuildID: build.ID, SiteID: build.SiteID, ProjectID: build.ProjectID,
		Image: NodeImage(service.options.NodeImage, settings.NodeVersion), CloneURL: service.provider.CloneURL(repository),
		Commit: build.CommitSHA, CloneToken: credential.Token, RootDir: settings.RootDir, OutputDir: settings.OutputDir,
		InstallCommand: settings.InstallCommand, BuildCommand: settings.BuildCommand,
		CPUMillis: limits.CPUMillis, Memory: limits.Memory, Disk: limits.Disk, Timeout: time.Duration(limits.TimeoutSeconds) * time.Second,
		Env: env, LargestPlan: largest,
	}, revoke, nil
}

// environment is the site's declared variables plus the project secrets marked build-visible.
func (e *execution) environment(ctx context.Context) (map[string]string, error) {
	service, build := e.dispatcher.service, e.build
	env := make(map[string]string, len(build.Settings.Env))
	for name, value := range build.Settings.Env {
		env[name] = value
	}
	visible, err := service.store.VisibleSecrets(ctx, build.ProjectID)
	if err != nil || len(visible) == 0 {
		return env, err
	}
	values, err := service.secrets.Secrets(ctx, build.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, name := range visible {
		if value, held := values[name]; held {
			env[name] = value
			e.secrets[name] = value
		}
	}
	return env, nil
}

func (e *execution) remove(ctx context.Context) {
	e.refreshLog(ctx)
	if err := e.dispatcher.runner.Remove(context.WithoutCancel(ctx), e.build.ID); err != nil {
		log.Printf("site build %s: remove its job: %v", e.build.ID, err)
	}
}

// await polls the pod until the output is packed or the build failed; cloned runs once the clone step is done.
func (e *execution) await(ctx context.Context, cloned func()) RunStatus {
	options := e.dispatcher.options
	lastLog := time.Time{}
	for {
		status, err := e.dispatcher.runner.Status(ctx, e.build.ID)
		if err != nil {
			log.Printf("site build %s: read status: %v", e.build.ID, err)
		} else if status.Phase == RunReady || status.Phase == RunFailed {
			return status
		} else if status.CloneDone {
			cloned()
		}
		if time.Since(lastLog) >= options.LogEvery {
			e.refreshLog(ctx)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return RunStatus{Phase: RunFailed, Failure: FailPlatform, Detail: stuckBuildReason}
			}
			return RunStatus{Phase: RunFailed, Failure: FailPlatform, Detail: "the build was stopped"}
		case <-time.After(options.StatusPoll):
		}
	}
}

// refreshLog keeps the latest tail, so a build whose pod is gone still has one.
func (e *execution) refreshLog(ctx context.Context) {
	logs, err := e.dispatcher.runner.Logs(context.WithoutCancel(ctx), e.build.ID)
	if err != nil {
		log.Printf("site build %s: read logs: %v", e.build.ID, err)
		return
	}
	if logs == "" {
		return
	}
	e.log = logs
	err = e.dispatcher.service.store.SaveLog(context.WithoutCancel(ctx), e.build.ID, e.dispatcher.options.Holder, redact(logs, e.secrets))
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		log.Printf("site build %s: save logs: %v", e.build.ID, err)
	}
}

// deploy collects the packed output and hands it to the Sites deploy path, every upload check included.
func (e *execution) deploy(ctx context.Context) outcome {
	service, build := e.dispatcher.service, e.build
	ceiling, err := service.sites.MaxUploadBytes(ctx, build.ProjectID)
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	spool, err := os.CreateTemp(e.dispatcher.options.SpoolDir, "site-build-*.tar.gz")
	if err != nil {
		return failedWith(FailPlatform, userMessage(err))
	}
	defer func() {
		_ = spool.Close()
		if err := os.Remove(spool.Name()); err != nil {
			log.Printf("site build %s: remove spool: %v", build.ID, err)
		}
	}()
	if err := e.dispatcher.runner.Collect(ctx, build.ID, spool, ceiling); err != nil {
		return failedWith(FailDeploy, userMessage(err))
	}
	e.refreshLog(ctx)
	deploy, err := e.deployWithRetry(ctx, spool)
	if err != nil {
		return failedWith(FailDeploy, userMessage(err))
	}
	return outcome{status: StatusSucceeded, deployID: deploy.ID}
}

// busy are refusals that clear once another deploy of the project or site finishes.
func busy(err error) bool {
	return errors.Is(err, sites.ErrProjectDeployRunning) || errors.Is(err, sites.ErrTooManyDeploys) || errors.Is(err, sites.ErrSiteBusy)
}

func (e *execution) deployWithRetry(ctx context.Context, spool *os.File) (*sites.Deploy, error) {
	service, build, options := e.dispatcher.service, e.build, e.dispatcher.options
	var lastErr error
	for try := 0; try < options.DeployTries; try++ {
		if _, err := spool.Seek(0, 0); err != nil {
			return nil, err
		}
		deploy, err := service.sites.Deploy(ctx, sites.DeployRequest{
			ProjectID: build.ProjectID, SiteID: build.SiteID, Actor: build.RequestedBy, Label: build.DeployLabel(), Body: spool, SlotTaken: true,
		})
		if !busy(err) {
			return deploy, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(options.DeployRetry):
		}
	}
	return nil, lastErr
}
