package dockerbuilds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
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
	// egressGraceSeconds keeps the sidecar up a little past the steps' time limit.
	egressGraceSeconds = 60
)

// chain is the order the steps run in, each once the one before exits 0.
var chain = []string{stepPrepare, stepClone, stepBuild, stepCollect}

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

// Start lays out the build's sandbox — volume, networks, sidecar, disk watcher — creates every step,
// then starts the first; Status starts the rest in turn.
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
	site := r.siteBox(spec)
	proxy, err := r.openBox(ctx, site)
	if err != nil {
		return err
	}
	for _, step := range chain {
		if err := r.create(ctx, spec.BuildID, step, r.stepContainer(site, spec, step, proxy)); err != nil {
			return err
		}
	}
	return r.start(ctx, spec.BuildID, stepPrepare)
}

// siteBox is the sandbox a site build runs in, labelled with its site and project.
func (r *Runner) siteBox(spec sitebuilds.RunSpec) box {
	return r.newBox(spec.BuildID, componentBuild, map[string]string{labelSite: spec.SiteID, labelProject: spec.ProjectID},
		spec.Timeout, resource.MustParse(spec.Disk))
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

type stepContainer struct {
	config *container.Config
	host   *container.HostConfig
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

func (r *Runner) stepContainer(site box, spec sitebuilds.RunSpec, step, proxy string) stepContainer {
	size := toolSize[step]
	memory, pids := size.memory, toolPids
	if step == stepBuild {
		parsed := resource.MustParse(spec.Memory)
		size.cpuMillis, memory, pids = int64(spec.CPUMillis), parsed.Value(), buildPids
	}
	online := step == stepClone || step == stepBuild
	planned := r.boxStep(site, spec.Image, size.cpuMillis, memory, pids, online)
	config := planned.config
	switch step {
	case stepPrepare:
		config.Image, config.User, config.Cmd = r.opts.PrepareImage, "0:0", []string{"chmod", "1777", workDir}
	case stepClone:
		config.Env = append([]string{"CLONE_URL=" + spec.CloneURL, "COMMIT=" + spec.Commit, "HOME=/work/home",
			"GIT_TERMINAL_PROMPT=0", "GIT_TOKEN=" + spec.CloneToken}, proxyEnv(proxy)...)
	case stepBuild:
		planned.host.CPUShares = buildShares
		config.Env = buildEnv(spec, proxy)
	case stepCollect:
		config.Env = []string{"ROOT_DIR=" + spec.RootDir, "OUTPUT_DIR=" + spec.OutputDir}
	}
	if step != stepPrepare {
		config.Cmd = site.deadlined("bash", "-c", r.scripts[step])
	}
	return planned
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

func (r *Runner) create(ctx context.Context, buildID, step string, planned stepContainer) error {
	if _, err := r.engine.ContainerCreate(ctx, planned.config, planned.host, nil, nil, containerName(buildID, step)); err != nil {
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
