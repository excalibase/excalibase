package dockerbuilds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/containerd/errdefs"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// ErrDockerfileBuildsNeedGVisor refuses a Dockerfile build where BuildKit cannot run as it does on Kubernetes:
// it needs privilege escalation and two capabilities, which only gVisor makes safe, on Docker.
var ErrDockerfileBuildsNeedGVisor = errors.New("Dockerfile builds need Docker with a gVisor runtime (set APP_SANDBOX_RUNTIME to it); " +
	"this host can build apps without a Dockerfile with Buildpacks instead")

const (
	componentApp = "app-build"
	labelApp     = "excalibase.app"
)

// AppOptions are the app builds' images and clone schemes; the sandbox settings are the Runner's.
type AppOptions struct {
	// ToolImage runs the clone (bash and git).
	ToolImage string
	// BuildKitImage is moby/buildkit's rootless image, run daemonless.
	BuildKitImage string
	// BuilderImage is the Paketo builder; RunImage the image it builds onto; CraneImage fetches the run image.
	BuilderImage, RunImage, CraneImage string
	// CloneSchemes are the clone URL schemes a build may use; empty is https only.
	CloneSchemes []string
	// Platform is the host's os/arch; empty is linux/amd64.
	Platform string
}

func (o AppOptions) validate() error {
	for setting, image := range map[string]string{"tool image": o.ToolImage, "BuildKit image": o.BuildKitImage,
		"builder image": o.BuilderImage, "run image": o.RunImage, "crane image": o.CraneImage} {
		if strings.TrimSpace(image) == "" {
			return fmt.Errorf("app builds on a single host need the %s", setting)
		}
	}
	return nil
}

func (o AppOptions) platform() string {
	if o.Platform == "" {
		return k8s.AppBuildDefaultPlatform
	}
	return o.Platform
}

// AppRunner runs app builds (EXC-677) in the Runner's sandbox: the app build pod's steps, one container each.
type AppRunner struct {
	*Runner
	app AppOptions
}

var _ appbuilds.Cluster = (*AppRunner)(nil)

func NewAppRunner(runner *Runner, opts AppOptions) (*AppRunner, error) {
	if runner == nil {
		return nil, errors.New("app builds on a single host need the build runner")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return &AppRunner{Runner: runner, app: opts}, nil
}

// Supports names why this host cannot run builder: Dockerfile builds only on Docker under a sandbox runtime.
func (a *AppRunner) Supports(builder appbuilds.Builder) error {
	if builder == appbuilds.BuilderDockerfile && (a.Runner.opts.SandboxRuntime == "" || a.Runner.opts.Isolate) {
		return ErrDockerfileBuildsNeedGVisor
	}
	return nil
}

// Start lays out the build's sandbox, creates every step of its builder, then starts the first; Status starts the rest.
func (a *AppRunner) Start(ctx context.Context, spec appbuilds.RunSpec) error {
	if err := a.checkApp(spec); err != nil {
		return err
	}
	if err := a.Remove(ctx, spec.BuildID); err != nil {
		return fmt.Errorf("clear an earlier attempt: %w", err)
	}
	for _, ref := range a.images(spec.Builder) {
		if err := a.pull(ctx, ref); err != nil {
			return err
		}
	}
	disk := resource.MustParse(spec.Disk)
	sandbox := a.newBox(spec.BuildID, componentApp, map[string]string{labelApp: spec.AppID, labelProject: spec.ProjectID}, spec.Timeout, disk)
	proxy, err := a.openBox(ctx, sandbox)
	if err != nil {
		return err
	}
	for _, name := range planFor(spec.Builder).chain {
		if err := a.create(ctx, spec.BuildID, name, a.appStep(sandbox, spec, name, proxy)); err != nil {
			return err
		}
	}
	return a.start(ctx, spec.BuildID, stepPrepare)
}

func (a *AppRunner) checkApp(spec appbuilds.RunSpec) error {
	if err := spec.Validate(a.app.CloneSchemes...); err != nil {
		return err
	}
	if !buildIDPattern.MatchString(spec.BuildID) {
		return fmt.Errorf("build id %q cannot name containers", spec.BuildID)
	}
	if quantity := resource.MustParse(spec.Memory); quantity.Sign() <= 0 {
		return errors.New("the build's memory must be above zero")
	}
	if quantity := resource.MustParse(spec.Disk); quantity.Sign() <= 0 {
		return errors.New("the build's disk must be above zero")
	}
	return a.Supports(spec.Builder)
}

// images are what a build of this builder pulls first; the engine would pull the rest lazily.
func (a *AppRunner) images(builder appbuilds.Builder) []string {
	if builder == appbuilds.BuilderDockerfile {
		return []string{a.Runner.opts.PrepareImage, a.app.ToolImage, a.app.BuildKitImage}
	}
	return []string{a.Runner.opts.PrepareImage, a.app.ToolImage, a.app.BuilderImage, a.app.CraneImage}
}

// run is the build with the steps its builder made: a Dockerfile build has a buildkit step, a Buildpacks one does not.
func (a *AppRunner) run(ctx context.Context, buildID string) (buildRun, error) {
	_, err := a.engine.ContainerInspect(ctx, containerName(buildID, k8s.AppBuildStepBuildKit))
	if err != nil && !errdefs.IsNotFound(err) {
		return buildRun{}, fmt.Errorf("inspect the build's steps: %w", err)
	}
	plan := planFor(appbuilds.BuilderBuildpacks)
	if err == nil {
		plan = planFor(appbuilds.BuilderDockerfile)
	}
	return buildRun{Runner: a.Runner, id: buildID, plan: plan}, nil
}

func (a *AppRunner) Status(ctx context.Context, buildID string) (sitebuilds.RunStatus, error) {
	run, err := a.run(ctx, buildID)
	if err != nil {
		return sitebuilds.RunStatus{}, err
	}
	return run.statusOf(ctx)
}

func (a *AppRunner) Logs(ctx context.Context, buildID string) (string, error) {
	run, err := a.run(ctx, buildID)
	if err != nil {
		return "", err
	}
	return a.logsOf(ctx, buildID, run.plan.logs)
}

// Collect streams the OCI archive out of the collect step into w, failing past limit bytes.
func (a *AppRunner) Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error {
	return a.collectFrom(ctx, buildID, k8s.AppBuildArchivePath, w, limit)
}

// Remove deletes the build's containers, work volume and network, whichever builder made them.
func (a *AppRunner) Remove(ctx context.Context, buildID string) error {
	return a.removeBox(ctx, buildID, allAppSteps)
}
