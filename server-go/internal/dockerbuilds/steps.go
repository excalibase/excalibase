package dockerbuilds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	stepEgress  = "egress"
	stepWatch   = "watch"
	stepPrepare = "prepare"
	stepClone   = "clone"
	stepBuild   = "build"
	stepCollect = "collect"

	labelManaged   = "excalibase.managed"
	labelComponent = "excalibase.component"
	labelBuild     = "excalibase.build"
	labelSite      = "excalibase.site"
	labelProject   = "excalibase.project"
	labelDeadline  = "excalibase.build.deadline"
	labelDisk      = "excalibase.build.disk"
	componentBuild = "site-build"

	buildUser    = "1000:1000"
	egressPort   = "3128"
	workDir      = "/work"
	buildPids    = int64(4096)
	toolPids     = int64(256)
	buildShares  = int64(256)
	tmpfsOptions = "rw,nosuid,nodev,size=1g,mode=1777"
	mib          = int64(1) << 20
)

// chain is the order the steps run in, each once the one before exits 0.
var chain = []string{stepPrepare, stepClone, stepBuild, stepCollect}

// everyContainer is the chain and the two that run beside it from the start.
var everyContainer = append([]string{stepEgress, stepWatch}, chain...)

// stepLogs caps each container's log, so a build printing without end cannot fill the host's disk.
var stepLogs = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}}

// Exit codes of the disk watcher.
const (
	watchOverDisk     = 3
	watchHostShort    = 4
	watchUnmeasurable = 5
)

// watchScript measures /work from outside the steps: their code can neither kill it nor hide files from it
// (a folder it cannot read fails the build), and it also stops the build when the host runs short of disk.
const watchScript = `end=$(( $(date +%%s) + %d )); fails=0
while [ "$(date +%%s)" -lt "$end" ]; do
  free=$(df -Pk /work | awk 'NR==2 {print $4}')
  [ -n "$free" ] && [ "$free" -ge %d ] || exit 4
  if out=$(du -sxk /work 2>/dev/null); then
    fails=0; set -- $out; [ "$1" -le %d ] || exit 3
  else
    fails=$((fails + 1)); [ "$fails" -lt 3 ] || exit 5
  fi
  sleep %d
done`

// toolSize is a step's fixed CPU (millis) and memory; the build step's come from the plan.
// Under gVisor the sandbox's own memory counts against the limit, so these leave room for it.
var toolSize = map[string]struct {
	cpuMillis int64
	memory    int64
}{
	stepEgress: {500, 128 * mib}, stepWatch: {100, 32 * mib}, stepPrepare: {250, 128 * mib}, stepClone: {1000, 512 * mib}, stepCollect: {500, 512 * mib},
}

// proxyVariables point every tool a build runs at the egress sidecar; the network has no other way out.
var proxyVariables = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
	"npm_config_proxy", "npm_config_https_proxy", "YARN_HTTP_PROXY", "YARN_HTTPS_PROXY",
}

// Start lays out the build — volume, networks, sidecar and every step created —
// then starts the first step; Status starts the rest in turn.
func (r *Runner) Start(ctx context.Context, spec sitebuilds.RunSpec) error {
	if err := checkSpec(spec); err != nil {
		return err
	}
	if err := r.Remove(ctx, spec.BuildID); err != nil {
		return fmt.Errorf("clear an earlier attempt: %w", err)
	}
	for _, ref := range []string{spec.Image, r.opts.PrepareImage} {
		if err := r.pull(ctx, ref); err != nil {
			return err
		}
	}
	if err := r.layOut(ctx, spec); err != nil {
		return err
	}
	if err := r.startWatch(ctx, spec); err != nil {
		return err
	}
	proxy, err := r.startEgress(ctx, spec)
	if err != nil {
		return err
	}
	for _, step := range chain {
		if err := r.create(ctx, spec, step, r.stepContainer(spec, step, proxy)); err != nil {
			return err
		}
	}
	return r.start(ctx, spec.BuildID, stepPrepare)
}

func checkSpec(spec sitebuilds.RunSpec) error {
	if !buildIDPattern.MatchString(spec.BuildID) {
		return fmt.Errorf("build id %q cannot name containers", spec.BuildID)
	}
	if spec.Image == "" || spec.CPUMillis <= 0 || spec.Timeout <= 0 {
		return errors.New("a build needs an image, CPU and a time limit")
	}
	for name, value := range map[string]string{"memory": spec.Memory, "disk": spec.Disk} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return fmt.Errorf("the build's %s %q is not a size: %w", name, value, err)
		}
		if quantity.Sign() <= 0 {
			return fmt.Errorf("the build's %s must be above zero", name)
		}
	}
	return nil
}

func (r *Runner) layOut(ctx context.Context, spec sitebuilds.RunSpec) error {
	if err := r.ensureEgressNetwork(ctx); err != nil {
		return err
	}
	labels := r.labels(spec)
	if _, err := r.engine.NetworkCreate(ctx, r.buildNetwork(spec.BuildID), r.buildNetworkOptions(labels)); err != nil {
		return fmt.Errorf("create the build network: %w", err)
	}
	if _, err := r.engine.VolumeCreate(ctx, volume.CreateOptions{Name: r.workVolume(spec.BuildID), Driver: "local", Labels: labels}); err != nil {
		return fmt.Errorf("create the work volume: %w", err)
	}
	return nil
}

// ensureEgressNetwork makes the one network with a route out, which only sidecars join.
func (r *Runner) ensureEgressNetwork(ctx context.Context) error {
	name := r.egressNetwork()
	if _, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect the egress network: %w", err)
	}
	options := network.CreateOptions{Driver: "bridge", Labels: map[string]string{labelManaged: "true", labelComponent: componentBuild}}
	if r.opts.Isolate {
		options.Options = map[string]string{"isolate": "true"}
	}
	if _, err := r.engine.NetworkCreate(ctx, name, options); err != nil && !errdefs.IsConflict(err) {
		return fmt.Errorf("create the egress network: %w", err)
	}
	return nil
}

// startEgress runs the sidecar and answers the proxy URL the build steps use, by address:
// under gVisor a step may not reach the engine's resolver.
func (r *Runner) startEgress(ctx context.Context, spec sitebuilds.RunSpec) (string, error) {
	config := &container.Config{
		Image: r.opts.EgressImage, User: buildUser, Entrypoint: []string{"/bin/excalibase-build-egress"},
		Env: []string{
			"EGRESS_LISTEN=:" + egressPort,
			"EGRESS_EXTRA_DENY_CIDRS=" + strings.Join(r.opts.ExtraDenyCIDRs, ","),
			"EGRESS_ALLOW=" + strings.Join(r.opts.EgressAllow, ","),
			"EGRESS_LIFETIME=" + strconv.Itoa(lifetime(spec)) + "s",
		},
		Labels: r.labels(spec),
	}
	host := lockedHost(toolSize[stepEgress].cpuMillis, toolSize[stepEgress].memory, toolPids)
	host.NetworkMode = container.NetworkMode(r.buildNetwork(spec.BuildID))
	if err := r.create(ctx, spec, stepEgress, stepContainer{config: config, host: host}); err != nil {
		return "", err
	}
	name := containerName(spec.BuildID, stepEgress)
	if err := r.engine.NetworkConnect(ctx, r.egressNetwork(), name, nil); err != nil {
		return "", fmt.Errorf("connect the egress sidecar: %w", err)
	}
	if err := r.start(ctx, spec.BuildID, stepEgress); err != nil {
		return "", err
	}
	inspect, err := r.engine.ContainerInspect(ctx, name)
	if err != nil {
		return "", fmt.Errorf("inspect the egress sidecar: %w", err)
	}
	if inspect.NetworkSettings == nil {
		return "", errors.New("the engine answered no networks for the egress sidecar")
	}
	endpoint := inspect.NetworkSettings.Networks[r.buildNetwork(spec.BuildID)]
	if endpoint == nil || endpoint.IPAddress == "" {
		return "", errors.New("the egress sidecar has no address on the build network")
	}
	return "http://" + endpoint.IPAddress + ":" + egressPort, nil
}

// startWatch runs the disk watcher as the steps' own user, so it reads every folder they make (their 0700
// HOME too); one they make unreadable fails the build. No network, the volume read-only, its own PID namespace.
// It runs no build code, so it stays on the default runtime, where df sees the host's filesystem.
func (r *Runner) startWatch(ctx context.Context, spec sitebuilds.RunSpec) error {
	disk := resource.MustParse(spec.Disk)
	script := fmt.Sprintf(watchScript, lifetime(spec), r.opts.HostDiskFloor/1024, disk.Value()/1024, int(r.opts.DiskCheckEvery.Seconds()))
	config := &container.Config{Image: r.opts.PrepareImage, User: buildUser, Cmd: []string{"sh", "-c", script}, Labels: r.labels(spec)}
	host := lockedHost(toolSize[stepWatch].cpuMillis, toolSize[stepWatch].memory, toolPids)
	host.NetworkMode = "none"
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.workVolume(spec.BuildID), Target: workDir, ReadOnly: true}}
	if err := r.create(ctx, spec, stepWatch, stepContainer{config: config, host: host}); err != nil {
		return err
	}
	return r.start(ctx, spec.BuildID, stepWatch)
}

// lifetime is the build's time limit in whole seconds, which the engine enforces on each step.
func lifetime(spec sitebuilds.RunSpec) int {
	return int(spec.Timeout.Seconds())
}

type stepContainer struct {
	config *container.Config
	host   *container.HostConfig
}

func (r *Runner) labels(spec sitebuilds.RunSpec) map[string]string {
	deadline := r.opts.Now().Add(spec.Timeout).Unix()
	return map[string]string{
		labelManaged: "true", labelComponent: componentBuild, labelBuild: spec.BuildID,
		labelSite: spec.SiteID, labelProject: spec.ProjectID, labelDeadline: strconv.FormatInt(deadline, 10), labelDisk: spec.Disk,
	}
}

// lockedHost is what every step gets: no capability, no new privileges, read-only root, bounded processes.
func lockedHost(cpuMillis, memory, pids int64) *container.HostConfig {
	return &container.HostConfig{
		ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, LogConfig: stepLogs,
		Resources: container.Resources{
			NanoCPUs: cpuMillis * 1_000_000, Memory: memory, MemorySwap: memory, PidsLimit: &pids,
		},
	}
}

func (r *Runner) stepContainer(spec sitebuilds.RunSpec, step, proxy string) stepContainer {
	size := toolSize[step]
	memory, pids, shares := size.memory, toolPids, int64(0)
	if step == stepBuild {
		parsed := resource.MustParse(spec.Memory)
		size.cpuMillis, memory, pids, shares = int64(spec.CPUMillis), parsed.Value(), buildPids, buildShares
	}
	host := lockedHost(size.cpuMillis, memory, pids)
	host.CPUShares = shares
	host.Runtime = r.opts.SandboxRuntime
	host.Tmpfs = map[string]string{"/tmp": tmpfsOptions}
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.workVolume(spec.BuildID), Target: workDir}}
	host.NetworkMode = "none"
	config := &container.Config{Image: spec.Image, User: buildUser, WorkingDir: workDir, Labels: r.labels(spec)}
	switch step {
	case stepPrepare:
		config.Image, config.User, config.Cmd = r.opts.PrepareImage, "0:0", []string{"chmod", "1777", workDir}
	case stepClone:
		host.NetworkMode = container.NetworkMode(r.buildNetwork(spec.BuildID))
		config.Env = append([]string{"CLONE_URL=" + spec.CloneURL, "COMMIT=" + spec.Commit, "HOME=/work/home",
			"GIT_TERMINAL_PROMPT=0", "GIT_TOKEN=" + spec.CloneToken}, proxyEnv(proxy)...)
	case stepBuild:
		host.NetworkMode = container.NetworkMode(r.buildNetwork(spec.BuildID))
		config.Env = buildEnv(spec, proxy)
	case stepCollect:
		config.Env = []string{"ROOT_DIR=" + spec.RootDir, "OUTPUT_DIR=" + spec.OutputDir}
	}
	if step != stepPrepare {
		// The engine kills the step at the time limit, even with provisioning gone.
		config.Cmd = []string{"timeout", "-s", "KILL", strconv.Itoa(lifetime(spec)), "bash", "-c", r.scripts[step]}
	}
	return stepContainer{config: config, host: host}
}

// loadScripts reads the Job's own scripts for the steps that run one.
func loadScripts() (map[string]string, error) {
	scripts := map[string]string{}
	for _, step := range []string{stepClone, stepBuild, stepCollect} {
		body, err := k8s.SiteBuildScript(step + ".sh")
		if err != nil {
			return nil, err
		}
		scripts[step] = body
	}
	return scripts, nil
}

func proxyEnv(proxy string) []string {
	env := make([]string, 0, len(proxyVariables)+2)
	for _, name := range proxyVariables {
		env = append(env, name+"="+proxy)
	}
	return append(env, "NO_PROXY=", "NODE_USE_ENV_PROXY=1")
}

// buildEnv is the declared variables, then the fixed ones, which win: a declared HOME or proxy is dropped.
func buildEnv(spec sitebuilds.RunSpec, proxy string) []string {
	fixed := map[string]string{}
	var tail []string
	for _, pair := range k8s.SiteBuildStepEnv(spec) {
		fixed[pair.Name] = pair.Value
		tail = append(tail, pair.Name+"="+pair.Value)
	}
	for _, pair := range proxyEnv(proxy) {
		name, value, _ := strings.Cut(pair, "=")
		fixed[name] = value
		tail = append(tail, pair)
	}
	names := make([]string, 0, len(spec.Env))
	for name := range spec.Env {
		if _, taken := fixed[name]; !taken {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	env := make([]string, 0, len(names)+len(tail))
	for _, name := range names {
		env = append(env, name+"="+spec.Env[name])
	}
	return append(env, tail...)
}

func (r *Runner) create(ctx context.Context, spec sitebuilds.RunSpec, step string, planned stepContainer) error {
	if _, err := r.engine.ContainerCreate(ctx, planned.config, planned.host, nil, nil, containerName(spec.BuildID, step)); err != nil {
		return fmt.Errorf("create the %s step: %w", step, err)
	}
	return nil
}

func (r *Runner) start(ctx context.Context, buildID, step string) error {
	if err := r.engine.ContainerStart(ctx, containerName(buildID, step), container.StartOptions{}); err != nil {
		return fmt.Errorf("start the %s step: %w", step, err)
	}
	return nil
}

// pull fetches a build image; the refs are pinned by digest unless the operator mirrors them.
func (r *Runner) pull(ctx context.Context, ref string) error {
	stream, err := r.engine.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("%w: pull %s: %v", sitebuilds.ErrImagePull, ref, err)
	}
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: pull %s: %v", sitebuilds.ErrImagePull, ref, err)
		}
		if detail := strings.TrimSpace(message.Error + " " + message.ErrorDetail.Message); detail != "" {
			return fmt.Errorf("%w: pull %s: %s", sitebuilds.ErrImagePull, ref, detail)
		}
	}
}
