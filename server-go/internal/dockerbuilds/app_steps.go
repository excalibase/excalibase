package dockerbuilds

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	// secretEnvPrefix carries a build-visible secret into the step that writes it to a file and forgets the variable.
	secretEnvPrefix = "EXC_SECRET_"
	secretsTmpfs    = "rw,noexec,nosuid,nodev,size=8m,mode=1777"
	appPlatformEnv  = k8s.AppBuildPlatformDir + "/env"
)

var cnbUser = strconv.Itoa(k8s.AppBuildCNBUID) + ":" + strconv.Itoa(k8s.AppBuildCNBUID)

func init() {
	for step, size := range map[string]struct {
		cpuMillis int64
		memory    int64
	}{stepShare: {100, 32 * mib}, stepRunImage: {500, 512 * mib}, stepAnalyze: {500, 512 * mib}, stepExport: {500, 512 * mib}} {
		toolSize[step] = size
	}
}

// appStep is one step of an app build in the box: the app build pod's step, with the sandbox's hardening.
func (a *AppRunner) appStep(sandbox box, spec appbuilds.RunSpec, name, proxy string) stepContainer {
	heavy := name == stepBuildKit || name == stepPacks
	size := toolSize[name]
	memory, pids := size.memory, toolPids
	if heavy {
		limit := resource.MustParse(spec.Memory)
		size.cpuMillis, memory, pids = int64(spec.CPUMillis), limit.Value(), buildPids
	}
	planned := a.boxStep(sandbox, a.appImage(name), size.cpuMillis, memory, pids, planFor(spec.Builder).online[name])
	if heavy {
		planned.host.CPUShares = buildShares
	}
	switch name {
	case stepPrepare:
		planned.config.User, planned.config.Cmd = "0:0", []string{"sh", "-c", k8s.AppBuildPrepareScript}
	case stepClone:
		a.cloneStep(sandbox, spec, proxy, planned)
	case stepShare:
		planned.config.Cmd = sandbox.deadlined("chmod", "-R", "a+rwX", appbuilds.WorkSource)
	case stepRunImage:
		a.runImageStep(proxy, planned)
	case stepBuildKit:
		a.buildKitStep(sandbox, spec, proxy, planned)
	case stepCollect:
		a.collectStep(sandbox, spec, planned)
	default:
		a.lifecycleStep(sandbox, spec, name, proxy, planned)
	}
	return planned
}

func (a *AppRunner) appImage(name string) string {
	switch name {
	case stepPrepare, stepShare, stepCollect:
		return a.Runner.opts.PrepareImage
	case stepClone:
		return a.app.ToolImage
	case stepBuildKit:
		return a.app.BuildKitImage
	case stepRunImage:
		return a.app.CraneImage
	}
	return a.app.BuilderImage
}

func (a *AppRunner) cloneStep(sandbox box, spec appbuilds.RunSpec, proxy string, planned stepContainer) {
	planned.config.Env = append([]string{"CLONE_URL=" + spec.CloneURL, "COMMIT=" + spec.Commit, "HOME=/work/home",
		"GIT_TERMINAL_PROMPT=0", "GIT_TOKEN=" + spec.CloneToken}, proxyEnv(proxy)...)
	planned.config.Cmd = sandbox.deadlined("bash", "-c", a.scripts[stepClone])
}

// runImageStep fetches the run image into the OCI layout; crane's image has no shell, so no time-limit wrapper:
// the status check and the other steps' limits still end a build that runs long.
func (a *AppRunner) runImageStep(proxy string, planned stepContainer) {
	planned.config.User = cnbUser
	planned.config.Entrypoint = []string{"crane"}
	planned.config.Cmd = k8s.AppBuildRunImageArgs(a.app.RunImage, a.app.platform())
	planned.config.Env = append([]string{"HOME=/work/cnb-home"}, proxyEnv(proxy)...)
}

// buildKitStep runs rootless BuildKit daemonless. Alone among the steps it keeps privilege escalation and adds
// SETUID and SETGID, for rootlesskit's newuidmap; Supports allows it only under a gVisor runtime.
func (a *AppRunner) buildKitStep(sandbox box, spec appbuilds.RunSpec, proxy string, planned stepContainer) {
	secrets := k8s.AppBuildSecretsDir
	entry := secretFiles(secrets, spec.Secrets) + k8s.AppBuildKitEntry
	args := append([]string{"build"}, proxyBuildArgs(proxy)...)
	spec.BuildArgs = withoutProxyArgs(spec.BuildArgs)
	args = append(args, k8s.AppBuildKitArgs(spec)[1:]...)
	planned.config.Entrypoint = sandbox.deadlined("sh", "-c", entry, "buildkit", spec.ContextDir(), spec.DockerfileFile())
	planned.config.Cmd = args
	planned.config.Env = slices.Concat(pairEnv(k8s.AppBuildKitEnv()), proxyEnv(proxy), secretEnv(spec.Secrets))
	planned.host.SecurityOpt = nil
	planned.host.CapAdd = []string{"SETUID", "SETGID"}
	planned.host.Tmpfs[secrets] = secretsTmpfs
}

// lifecycleStep runs one CNB lifecycle phase as the CNB user. Only the phase that runs the app's code
// gets the build-visible secrets (as the platform's env files) and the proxy.
func (a *AppRunner) lifecycleStep(sandbox box, spec appbuilds.RunSpec, name, proxy string, planned stepContainer) {
	script := ""
	for _, phase := range k8s.AppBuildpackPhases(a.app.RunImage) {
		if phase.Name == name {
			script = phase.Script
		}
	}
	env := pairEnv(k8s.AppBuildCNBEnv(spec))
	if name == stepPacks {
		script = secretFiles(appPlatformEnv, spec.Secrets) + script
		env = slices.Concat(env, proxyEnv(proxy), secretEnv(spec.Secrets))
		planned.host.Tmpfs[appPlatformEnv] = secretsTmpfs
	}
	planned.config.User = cnbUser
	planned.config.Entrypoint = sandbox.deadlined("bash", "-c", "set -e; "+script)
	planned.config.Env = env
}

// collectStep packs the image as whoever wrote it, then holds it for Collect until the ready mark carries its token.
func (a *AppRunner) collectStep(sandbox box, spec appbuilds.RunSpec, planned stepContainer) {
	script, err := k8s.AppBuildCollectScript()
	if err != nil {
		// The script is embedded in the binary: a missing one is a broken build of the platform.
		panic(err)
	}
	token := readyToken()
	if spec.Builder == appbuilds.BuilderBuildpacks {
		planned.config.User = cnbUser
	}
	planned.config.Labels = withLabel(planned.config.Labels, labelReady, token)
	planned.config.Env = []string{"BUILDER=" + string(spec.Builder), "IMAGE_DIR=" + k8s.AppBuildImageDir(), "READY_TOKEN=" + token}
	planned.config.Cmd = sandbox.deadlined("sh", "-c", script)
}

// secretFiles is the shell that writes each secret from its variable to a file in dir and unsets the variable.
// Names are validated variable names, so they are safe in the command.
func secretFiles(dir string, secrets map[string]string) string {
	var script strings.Builder
	fmt.Fprintf(&script, "mkdir -p %s; ", dir)
	for _, name := range sortedKeys(secrets) {
		fmt.Fprintf(&script, `printf %%s "$%[1]s%[2]s" > %[3]s/%[2]s; unset %[1]s%[2]s; `, secretEnvPrefix, name, dir)
	}
	return script.String() + "\n"
}

func secretEnv(secrets map[string]string) []string {
	env := make([]string, 0, len(secrets))
	for _, name := range sortedKeys(secrets) {
		env = append(env, secretEnvPrefix+name+"="+secrets[name])
	}
	return env
}

func pairEnv(pairs []k8s.EnvPair) []string {
	env := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		env = append(env, pair.Name+"="+pair.Value)
	}
	return env
}

// proxyBuildArgs hand the sidecar's address to the Dockerfile's own RUN steps; BuildKit keeps them out of the image's history.
func proxyBuildArgs(proxy string) []string {
	args := make([]string, 0, 8)
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		args = append(args, "--opt", "build-arg:"+name+"="+proxy)
	}
	return args
}

// withoutProxyArgs drops declared build arguments that name a proxy: the sidecar is the only way out.
func withoutProxyArgs(args map[string]string) map[string]string {
	kept := make(map[string]string, len(args))
	for name, value := range args {
		if !slices.Contains(proxyVariables, name) && !strings.EqualFold(name, "no_proxy") {
			kept[name] = value
		}
	}
	return kept
}

func sortedKeys(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
