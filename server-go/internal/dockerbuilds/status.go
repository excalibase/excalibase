package dockerbuilds

import (
	"archive/tar"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"strconv"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	// exitKilled is a step SIGKILLed, which on a single host is the memory limit.
	exitKilled  = 137
	stopSeconds = 5
)

// steps are the build's step containers as the engine sees them.
type steps map[string]*container.InspectResponse

func failed(kind sitebuilds.FailureKind, code int, detail string) sitebuilds.RunStatus {
	return sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: kind, ExitCode: int32(code), Detail: detail}
}

// Status reads the steps and starts the next one once the one before it has exited 0.
func (r *Runner) Status(ctx context.Context, buildID string) (sitebuilds.RunStatus, error) {
	return buildRun{Runner: r, id: buildID, plan: sitePlan}.statusOf(ctx)
}

// buildRun is one build and the steps its kind runs.
type buildRun struct {
	*Runner
	id   string
	plan stepPlan
}

func (b buildRun) statusOf(ctx context.Context) (sitebuilds.RunStatus, error) {
	current, err := b.inspectSteps(ctx)
	if err != nil {
		return sitebuilds.RunStatus{}, err
	}
	if current == nil {
		return failed(sitebuilds.FailPlatform, 0, "the build's containers are gone from the host"), nil
	}
	cloneDone := current[stepClone].State.Status == "exited"
	for _, step := range b.plan.chain {
		if status, decided := b.stepOutcome(ctx, step, current[step], cloneDone); decided {
			return status, nil
		}
	}
	return sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: cloneDone}, nil
}

// stepOutcome decides the build from one step; not decided means it exited 0 and the next step decides.
func (b buildRun) stepOutcome(ctx context.Context, step string, inspect *container.InspectResponse, cloneDone bool) (sitebuilds.RunStatus, bool) {
	state := inspect.State
	if b.killed(step, state) {
		b.stopAll(ctx)
		if !state.OOMKilled && b.pastDeadline(inspect) {
			return failed(sitebuilds.FailTimeout, 0, "the build ran past its time limit"), true
		}
		return failed(sitebuilds.FailOutOfMemory, 0, fmt.Sprintf("the %s step ran out of memory", step)), true
	}
	if state.Status == "exited" || state.Status == "dead" {
		if code := state.ExitCode; code != 0 {
			return failed(b.plan.failureKind(step, int32(code)), code, fmt.Sprintf("the %s step exited %d", step, code)), true
		}
		return sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, step == stepCollect
	}
	if state.Status == "created" {
		return b.startStep(ctx, step, inspect, cloneDone), true
	}
	// Only a running collect step's mark counts: build code can leave one on the shared volume.
	if step == stepCollect && b.packed(ctx, inspect.Config.Labels[labelReady]) {
		return sitebuilds.RunStatus{Phase: sitebuilds.RunReady}, true
	}
	if b.pastDeadline(inspect) {
		b.stopAll(ctx)
		return failed(sitebuilds.FailTimeout, 0, "the build ran past its time limit"), true
	}
	return b.whileRunning(ctx, step, inspect, cloneDone), true
}

// startStep starts a created step unless the build is already past its deadline.
func (b buildRun) startStep(ctx context.Context, step string, inspect *container.InspectResponse, cloneDone bool) sitebuilds.RunStatus {
	if b.pastDeadline(inspect) {
		b.stopAll(ctx)
		return failed(sitebuilds.FailTimeout, 0, "the build ran past its time limit")
	}
	if err := b.start(ctx, b.id, step); err != nil {
		log.Printf("build %s: %v", b.id, err)
		b.stopAll(ctx)
		return failed(sitebuilds.FailPlatform, 0, fmt.Sprintf("the %s step could not start", step))
	}
	return sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: cloneDone}
}

// inspectSteps answers nil when any step is gone.
func (b buildRun) inspectSteps(ctx context.Context) (steps, error) {
	current := steps{}
	for _, step := range b.plan.chain {
		inspect, err := b.engine.ContainerInspect(ctx, containerName(b.id, step))
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("inspect the %s step: %w", step, err)
		}
		if inspect.ContainerJSONBase == nil || inspect.State == nil || inspect.Config == nil {
			return nil, fmt.Errorf("the engine answered no state for the %s step", step)
		}
		current[step] = &inspect
	}
	return current, nil
}

// pastDeadline: a step without a readable deadline counts as past it.
func (r *Runner) pastDeadline(inspect *container.InspectResponse) bool {
	deadline, err := strconv.ParseInt(inspect.Config.Labels[labelDeadline], 10, 64)
	return err != nil || r.opts.Now().Unix() > deadline
}

// killed: the engine saw the memory limit hit, or a step sized by the plan was SIGKILLed.
func (b buildRun) killed(step string, state *container.State) bool {
	return state.OOMKilled || (b.plan.limited[step] && state.Status == "exited" && state.ExitCode == exitKilled)
}

// whileRunning watches the disk watcher, and the sidecar the clone and build steps reach the internet through.
func (b buildRun) whileRunning(ctx context.Context, step string, inspect *container.InspectResponse, cloneDone bool) sitebuilds.RunStatus {
	if verdict, stopped := b.diskVerdict(ctx, inspect.Config.Labels[labelDisk]); stopped {
		b.stopAll(ctx)
		return verdict
	}
	if b.plan.online[step] {
		if !b.alive(ctx, stepEgress) {
			b.stopAll(ctx)
			return failed(sitebuilds.FailPlatform, 0, "the build's network proxy stopped")
		}
	}
	return sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: cloneDone}
}

// diskVerdict reads the disk watcher's exit; one still running, or done at its lifetime, is no verdict.
func (b buildRun) diskVerdict(ctx context.Context, disk string) (sitebuilds.RunStatus, bool) {
	watch, err := b.engine.ContainerInspect(ctx, containerName(b.id, stepWatch))
	if err != nil || watch.ContainerJSONBase == nil || watch.State == nil || watch.State.Running {
		return sitebuilds.RunStatus{}, false
	}
	switch watch.State.ExitCode {
	case watchOverDisk:
		return failed(sitebuilds.FailDisk, 0, "the build used more than its "+disk+" of disk"), true
	case watchHostShort:
		return failed(sitebuilds.FailDisk, 0, "the host is short of disk; the build was stopped"), true
	case watchUnmeasurable:
		return failed(sitebuilds.FailDisk, 0, "the build's files could not be measured; it may have made a folder nobody can read"), true
	}
	return sitebuilds.RunStatus{}, false
}

// alive: a container the engine shows running; one it cannot answer for is given the benefit of the doubt.
func (b buildRun) alive(ctx context.Context, name string) bool {
	inspect, err := b.engine.ContainerInspect(ctx, containerName(b.id, name))
	if err != nil {
		return true
	}
	return inspect.ContainerJSONBase != nil && inspect.State != nil && inspect.State.Running
}

// packed is collect's ready mark: the output is packed and waiting.
// packed: the collect step wrote its own token into the ready mark.
func (b buildRun) packed(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	stream, _, err := b.engine.CopyFromContainer(ctx, containerName(b.id, stepCollect), k8s.SiteBuildReadyPath)
	if err != nil {
		return false
	}
	defer stream.Close()
	archive := tar.NewReader(stream)
	if _, err := archive.Next(); err != nil {
		return false
	}
	mark, err := io.ReadAll(io.LimitReader(archive, int64(len(token))+1))
	return err == nil && subtle.ConstantTimeCompare(mark, []byte(token)) == 1
}

// stopAll stops every step, the sidecar and the watcher; Remove deletes them after.
func (b buildRun) stopAll(ctx context.Context) {
	b.stopBox(ctx, b.id, b.plan.chain)
}

// Remove deletes the build's containers, work volume and network; a build already gone is not an error.
func (r *Runner) Remove(ctx context.Context, buildID string) error {
	return r.removeBox(ctx, buildID, sitePlan.chain)
}

// ForgetCloneCredential has nothing to delete: the dispatcher revokes the clone token at the git host once
// the clone is done, but the revoked token stays in the exited clone container's config until Remove.
func (r *Runner) ForgetCloneCredential(context.Context, string) error {
	return nil
}

// logf is log.Printf, named for the box's own use.
var logf = log.Printf
