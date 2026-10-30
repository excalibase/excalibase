package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// buildKind is what a site build and an app build do differently inside one run.
type buildKind interface {
	jobs() JobControl
	// connection re-reads the build's connection; nil when it is gone.
	connection(ctx context.Context) (connected, error)
	// preflight refuses a build this install cannot run, before anything is minted.
	preflight(ctx context.Context) *outcome
	start(ctx context.Context, credential gitprovider.CloneCredential) error
	failureMessage(status RunStatus) string
	deliver(ctx context.Context) outcome
}

// execution is one build's run.
type execution struct {
	dispatcher *Dispatcher
	build      *Build
	kind       buildKind
	// secrets are hidden from every log saved: build-visible secrets, and an app's build arguments.
	secrets map[string]string
	log     string
	// notes are the dispatcher's own lines, kept ahead of the steps' output.
	notes string
	// attempt counts the runs started; runID names the current one's Job.
	attempts int
	runID    string
}

// maxAttempts is the first run and the one retry a sandbox start error earns.
const maxAttempts = 2

// runID names an attempt's Job: the build id, then a fresh name per retry, so a retry never meets the Job being deleted.
func runID(buildID string, attempt int) string {
	if attempt == 0 {
		return buildID
	}
	return fmt.Sprintf("%s-r%d", buildID, attempt)
}

func newExecution(dispatcher *Dispatcher, build *Build) *execution {
	run := &execution{dispatcher: dispatcher, build: build, secrets: map[string]string{}, runID: build.ID}
	switch {
	case build.Kind != KindApp:
		run.kind = &siteKind{execution: run, runner: dispatcher.runner}
	case dispatcher.apps != nil:
		run.kind = &appKind{execution: run, runner: dispatcher.apps.runner, publisher: dispatcher.apps.publisher}
	default:
		run.kind = unwiredApps{}
	}
	return run
}

func (e *execution) execute(ctx context.Context) outcome {
	if refused := e.checkSource(ctx); refused != nil {
		return *refused
	}
	if refused := e.kind.preflight(ctx); refused != nil {
		return *refused
	}
	status, refused := e.attempt(ctx)
	if refused == nil && status.StartFailed {
		e.remove(ctx)
		e.note(e.log + fmt.Sprintf("== retry ==\n%s; retried once with a fresh clone credential\n", status.Detail))
		e.log = ""
		status, refused = e.attempt(ctx)
	}
	defer e.remove(ctx)
	if refused != nil {
		return *refused
	}
	if status.Phase != RunReady {
		return failedWith(status.Failure, e.kind.failureMessage(status))
	}
	if reason := e.stillDeployable(ctx); reason != "" {
		return failedWith(FailPlatform, reason)
	}
	deployCtx, cancelDeploy := context.WithTimeout(ctx, e.dispatcher.options.DeployBudget)
	defer cancelDeploy()
	return e.kind.deliver(deployCtx)
}

// checkSource re-reads the connection, its connector and the repository's current name before anything runs.
func (e *execution) checkSource(ctx context.Context) *outcome {
	service, build := e.dispatcher.service, e.build
	connection, err := e.kind.connection(ctx)
	if err != nil {
		return refuse(FailPlatform, userMessage(err))
	}
	if connection == nil || connection.source().RepositoryID != build.RepositoryID {
		return refuse(FailPlatform, "the repository was disconnected before this build started")
	}
	if err := service.checkConnector(ctx, connection); err != nil {
		return refuse(FailClone, userMessage(err))
	}
	if err := service.currentRepository(ctx, connection); err != nil {
		return refusal(err)
	}
	build.Repository = connection.source().Repository
	return nil
}

// attempt mints a clone credential, starts the build's Job and waits for it; the credential goes once the clone is done.
func (e *execution) attempt(ctx context.Context) (RunStatus, *outcome) {
	e.runID = runID(e.build.ID, e.attempts)
	e.attempts++
	credential, err := e.mint(ctx)
	if err != nil {
		return RunStatus{}, refusal(err)
	}
	cloned := e.forgetOnce(ctx, credential)
	defer cloned()
	startCtx, cancelStart := context.WithTimeout(ctx, e.dispatcher.options.StartTimeout)
	err = e.kind.start(startCtx, credential)
	cancelStart()
	if err != nil {
		return RunStatus{}, refuse(FailPlatform, userMessage(err))
	}
	timeout := time.Duration(e.build.Limits.TimeoutSeconds)*time.Second + e.dispatcher.options.PendingGrace
	awaitCtx, cancelAwait := context.WithTimeout(ctx, timeout)
	defer cancelAwait()
	return e.await(awaitCtx, cloned), nil
}

func (e *execution) mint(ctx context.Context) (gitprovider.CloneCredential, error) {
	build := e.build
	credential, err := e.dispatcher.service.provider.CloneCredential(ctx, build.InstallationID, build.RepositoryID)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return gitprovider.CloneCredential{}, &InputError{Reason: "the GitHub App no longer covers " + build.Repository}
	}
	if err != nil {
		return gitprovider.CloneCredential{}, fmt.Errorf("mint a clone credential: %w", err)
	}
	return credential, nil
}

// forgetOnce revokes the clone token and deletes its Secret, once; neither failing stops the build.
func (e *execution) forgetOnce(ctx context.Context, credential gitprovider.CloneCredential) func() {
	var once sync.Once
	runID := e.runID
	return func() {
		once.Do(func() {
			quiet := context.WithoutCancel(ctx)
			if err := e.dispatcher.service.provider.RevokeCloneCredential(quiet, credential.Token); err != nil {
				log.Printf("%s build %s: revoke the clone credential: %v", e.build.Kind, e.build.ID, err)
			}
			if err := e.kind.jobs().ForgetCloneCredential(quiet, runID); err != nil {
				log.Printf("%s build %s: delete the clone secret: %v", e.build.Kind, e.build.ID, err)
			}
		})
	}
}

// stillDeployable re-reads the connection and its connector just before the deploy; it answers why not, or "".
func (e *execution) stillDeployable(ctx context.Context) string {
	still, err := e.kind.connection(ctx)
	if err != nil {
		return userMessage(err)
	}
	if still == nil || still.source().RepositoryID != e.build.RepositoryID {
		return "the repository was disconnected while this build ran; nothing was deployed"
	}
	if err := e.dispatcher.service.checkConnector(ctx, still); err != nil {
		return userMessage(err) + "; nothing was deployed"
	}
	return ""
}

// visibleSecrets reads the project secrets marked build-visible, hiding each from the logs.
func (e *execution) visibleSecrets(ctx context.Context) (map[string]string, error) {
	service, build := e.dispatcher.service, e.build
	found := map[string]string{}
	visible, err := service.store.VisibleSecrets(ctx, build.ProjectID)
	if err != nil || len(visible) == 0 {
		return found, err
	}
	values, err := service.secrets.Secrets(ctx, build.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, name := range visible {
		if value, held := values[name]; held {
			found[name] = value
		}
	}
	e.hide("", found)
	return found, nil
}

// hide adds values to the redaction list under label+name, so a build argument never displaces a secret of the same name.
func (e *execution) hide(label string, values map[string]string) {
	for name, value := range values {
		e.secrets[label+name] = value
	}
}

func (e *execution) note(line string) { e.notes += line }

func (e *execution) fullLog() string { return e.notes + e.log }

func (e *execution) remove(ctx context.Context) {
	e.refreshLog(ctx)
	if err := e.kind.jobs().Remove(context.WithoutCancel(ctx), e.runID); err != nil {
		log.Printf("%s build %s: remove its job: %v", e.build.Kind, e.build.ID, err)
	}
}

// await polls the pod until the output is packed or the build failed; cloned runs once the clone step is done.
func (e *execution) await(ctx context.Context, cloned func()) RunStatus {
	options := e.dispatcher.options
	lastLog := time.Time{}
	for {
		status, err := e.kind.jobs().Status(ctx, e.runID)
		if err != nil {
			log.Printf("%s build %s: read status: %v", e.build.Kind, e.build.ID, err)
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
	logs, err := e.kind.jobs().Logs(context.WithoutCancel(ctx), e.runID)
	if err != nil {
		log.Printf("%s build %s: read logs: %v", e.build.Kind, e.build.ID, err)
		return
	}
	if logs == "" {
		return
	}
	e.log = logs
	err = e.dispatcher.service.store.SaveLog(context.WithoutCancel(ctx), e.build.ID, e.dispatcher.options.Holder, redact(e.fullLog(), e.secrets))
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		log.Printf("%s build %s: save logs: %v", e.build.Kind, e.build.ID, err)
	}
}
