package k8s

import (
	"embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// The app build pod follows the site build pod (EXC-354) and the gVisor
// app-build spike (excalibase-knowledge docs/spikes/2026-10-29-gvisor-app-builds.md):
// prepare-workdir → wait-for-fence → clone → image build → collect. Only clone
// holds the git token; the build steps get the build-visible secrets as files
// and no step holds a platform credential. Each builder writes an OCI image to
// /work/out, which collect hands to the controller as one archive.

//go:embed app_build_scripts/*.sh
var appBuildScripts embed.FS

const (
	appBuildComponent   = "app-build"
	appBuildScriptsName = "app-build-scripts"
	appBuildEgressName  = "app-build-egress"
	appBuildQuotaName   = "app-build-slots"
	appBuildLabelApp    = "excalibase.io/app"
	appBuildArchivePath = "/work/collect/image.tar"
	appBuildSecretsDir  = "/run/build-secrets"
	appBuildBuildKit    = "buildkit"
	appBuildRunImage    = "run-image"
	// The Paketo builder image's CNB user; detect and build run user code as it.
	appBuildCNBUID    = int64(1001)
	appBuildLayoutDir = "/work/out/layout"
	appBuildImageRef  = "excalibase.local/app:build"
)

// The Buildpacks steps, in order: analyze and export are the platform's side;
// detect, restore and build run as one step with the app's code and the build-visible secrets.
const (
	appBuildShareCheckout = "share-checkout"
	appBuildAnalyze       = "analyze"
	appBuildBuildpacks    = "buildpacks"
	appBuildExport        = "export"
	// appBuildPlatformDir/env holds the build-visible secrets, one file each (the CNB platform spec).
	appBuildPlatformDir = "/run/platform"
)

// AppBuildOptions is where and how this install runs app builds.
type AppBuildOptions struct {
	Namespace string
	// SiteBuildNamespace must differ from Namespace: each kind's unscoped quota would count the other's pods.
	SiteBuildNamespace string
	RuntimeClass       string
	PriorityClass      string
	// Slots is how many app builds run at once; the quota backstops it.
	Slots int
	// PrepareImage runs the root chmod of the work dirs and the collect step.
	PrepareImage string
	// ToolImage runs the fence and the clone (bash and git).
	ToolImage string
	// BuildKitImage is moby/buildkit's rootless image, run daemonless in the pod.
	BuildKitImage string
	// BuilderImage is the Paketo builder; RunImage the image it builds onto.
	BuilderImage string
	RunImage     string
	// CraneImage fetches the run image into the OCI layout the exporter writes to.
	CraneImage string
	// CloneSchemes are the clone URL schemes a build may use; empty is https only.
	CloneSchemes []string
	// Platform is the nodes' os/arch; the lifecycle reads one image from the layout, not an index.
	Platform       string
	ExtraDenyCIDRs []string
}

const defaultAppBuildPlatform = "linux/amd64"

// Validate refuses options whose quota would collide with the site builds'.
func (o AppBuildOptions) Validate() error {
	switch {
	case o.Namespace == "":
		return errors.New("app builds need a namespace")
	case o.SiteBuildNamespace == "":
		return errors.New("app builds need the site builds' namespace to keep apart from it")
	case o.Namespace == o.SiteBuildNamespace:
		return fmt.Errorf("app builds need their own namespace: %q is the site builds' namespace, and the two quotas would block each other", o.Namespace)
	}
	return nil
}

func (o AppBuildOptions) platform() string {
	if o.Platform == "" {
		return defaultAppBuildPlatform
	}
	return o.Platform
}

func appBuildName(buildID string) string { return "app-build-" + buildID }

func appBuildLabels(spec appbuilds.RunSpec) map[string]string {
	return map[string]string{
		siteBuildComponentKey: appBuildComponent, siteBuildLabelBuild: spec.BuildID,
		appBuildLabelApp: spec.AppID, siteBuildLabelProject: spec.ProjectID,
	}
}

func renderAppBuildJob(options AppBuildOptions, spec appbuilds.RunSpec) *batchv1.Job {
	return buildJob(appBuildName(spec.BuildID), options.Namespace, appBuildLabels(spec), spec.Timeout, renderAppBuildPod(options, spec))
}

func renderAppBuildPod(options AppBuildOptions, spec appbuilds.RunSpec) corev1.PodSpec {
	inits := []corev1.Container{
		appPrepareContainer(options.PrepareImage), fenceContainer(options.ToolImage), cloneContainer(options.ToolImage, spec.CloneURL, spec.Commit),
	}
	if spec.Builder == appbuilds.BuilderDockerfile {
		inits = append(inits, buildKitContainer(options, spec))
	} else {
		inits = append(inits, buildpacksContainers(options, spec)...)
	}
	return buildPodSpec(options.RuntimeClass, options.PriorityClass, inits, appCollectContainer(options, spec), appBuildVolumes(spec))
}

// AppBuildPrepareScript makes the directories the image builders write to, each sticky and open to their two users.
const AppBuildPrepareScript = "mkdir -p " + appBuildDirs + " && chmod 1777 " + appBuildDirs

const appBuildDirs = "/work /tmp /work/out /work/layers /work/cnb-home"

// appPrepareContainer is the site builds' root chmod, plus the directories the
// image builders write to, each sticky and open to the builders' two users.
func appPrepareContainer(image string) corev1.Container {
	prepare := prepareWorkdirContainer(image)
	prepare.Command = []string{"sh", "-c", AppBuildPrepareScript}
	return prepare
}

func appBuildResources(spec appbuilds.RunSpec) corev1.ResourceRequirements {
	return planLimits(spec.CPUMillis, spec.Memory, spec.Disk)
}

func secretsMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: "build-secrets", MountPath: appBuildSecretsDir, ReadOnly: true}
}

// buildKitEntry checks the context and Dockerfile resolve inside the checkout
// (exit 13; a symlink in the repository cannot point the build elsewhere) and
// names a missing Dockerfile (exit 12) before starting the daemonless build.
const buildKitEntry = `inside() { real=$(realpath "$1" 2>/dev/null) && case "$real" in /work/src|/work/src/*) return 0;; esac; return 1; }
inside "$1" || { echo "the root directory is not inside the repository"; exit 13; }
test -f "$2" || { echo "no Dockerfile at ${2#/work/src/}"; exit 12; }
inside "$2" || { echo "the Dockerfile is not inside the repository"; exit 13; }
mkdir -p /work/out; shift 2; exec buildctl-daemonless.sh "$@"`

// buildKitArgs is the buildctl command line after the context and Dockerfile checks.
func buildKitArgs(spec appbuilds.RunSpec) []string {
	dockerfile := spec.DockerfileFile()
	args := []string{"build", "--frontend", "dockerfile.v0",
		"--local", "context=" + spec.ContextDir(), "--local", "dockerfile=" + path.Dir(dockerfile),
		"--opt", "filename=" + path.Base(dockerfile)}
	for _, name := range sortedNames(spec.BuildArgs) {
		args = append(args, "--opt", "build-arg:"+name+"="+spec.BuildArgs[name])
	}
	for _, name := range sortedNames(spec.Secrets) {
		args = append(args, "--secret", "id="+name+",src="+appBuildSecretsDir+"/"+name)
	}
	return append(args, "--output", "type=oci,dest=/work/out/image.tar")
}

func buildKitEnv() []EnvPair {
	return []EnvPair{
		{"BUILDKITD_FLAGS", "--oci-worker-no-process-sandbox --root /work/buildkit"},
		{"HOME", "/work/home"}, {"XDG_RUNTIME_DIR", "/tmp/run"},
		// The image points TMPDIR into its own home; the root filesystem is read-only.
		{"TMPDIR", "/tmp"},
	}
}

func envVars(pairs []EnvPair) []corev1.EnvVar {
	vars := make([]corev1.EnvVar, 0, len(pairs))
	for _, pair := range pairs {
		vars = append(vars, corev1.EnvVar{Name: pair.Name, Value: pair.Value})
	}
	return vars
}

// buildKitContainer runs one throwaway rootless buildkitd inside the sandbox
// (no daemon socket, nothing shared between builds) and writes an OCI archive.
// Secrets are files for RUN --mount=type=secret; without BuildKit's process
// sandbox any RUN step can read them too, so they are build-visible, not step-scoped.
// Build args are plain.
func buildKitContainer(options AppBuildOptions, spec appbuilds.RunSpec) corev1.Container {
	dockerfile := spec.DockerfileFile()
	args := buildKitArgs(spec)
	security := lockedDown()
	// rootlesskit maps the build's user namespace through the setuid newuidmap,
	// inside gVisor: escalation on, and only the two capabilities it hands out.
	security.AllowPrivilegeEscalation = pointerTo(true)
	security.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SETUID", "SETGID"}}
	return corev1.Container{
		Name: appBuildBuildKit, Image: options.BuildKitImage, Command: []string{"sh", "-c", buildKitEntry, "buildkit", spec.ContextDir(), dockerfile},
		Args:            args,
		Env:             envVars(buildKitEnv()),
		SecurityContext: security, Resources: appBuildResources(spec),
		VolumeMounts: append(workMounts(), secretsMount()),
	}
}

// buildpacksContainers are the CNB lifecycle phases, one container each (the
// kpack model), exporting to an OCI layout: no registry and no credential, so
// nothing user code can read is worth stealing. crane first fetches the run
// image into that layout, as our own step.
func buildpacksContainers(options AppBuildOptions, spec appbuilds.RunSpec) []corev1.Container {
	runRef := options.RunImage
	security := lockedDown()
	security.RunAsUser, security.RunAsGroup = pointerTo(appBuildCNBUID), pointerTo(appBuildCNBUID)
	fetch := corev1.Container{
		Name: appBuildRunImage, Image: options.CraneImage, Command: []string{"crane"},
		Args:            AppBuildRunImageArgs(runRef, options.platform()),
		SecurityContext: security, Resources: guaranteed("500m", "512Mi"), VolumeMounts: workMounts(),
	}
	// The clone step's user owns the checkout; the buildpacks write into it as the CNB user.
	share := corev1.Container{
		Name: appBuildShareCheckout, Image: options.PrepareImage, Command: []string{"chmod", "-R", "a+rwX", appbuilds.WorkSource},
		SecurityContext: lockedDown(), Resources: guaranteed("100m", "32Mi"), VolumeMounts: workMounts(),
	}
	containers := []corev1.Container{share, fetch}
	for _, phase := range AppBuildpackPhases(runRef) {
		resources, mounts := guaranteed("500m", "512Mi"), workMounts()
		if phase.Name == appBuildBuildpacks {
			resources = appBuildResources(spec)
			mounts = append(mounts, corev1.VolumeMount{Name: "build-secrets", MountPath: appBuildPlatformDir + "/env", ReadOnly: true})
		}
		containers = append(containers, corev1.Container{
			Name: phase.Name, Image: options.BuilderImage, Command: []string{"bash", "-c", "set -e; " + phase.Script},
			Env: envVars(AppBuildCNBEnv(spec)), SecurityContext: security, Resources: resources, VolumeMounts: mounts,
		})
	}
	return containers
}

// AppBuildRunImageArgs is crane's command line fetching the run image into the OCI layout the exporter writes to.
func AppBuildRunImageArgs(runRef, platform string) []string {
	return []string{"pull", "--platform=" + platform, "--format=oci", runRef, path.Join(appBuildLayoutDir, layoutPath(runRef))}
}

// AppBuildCNBEnv is the lifecycle steps' variables; the root directory is read from APP_DIR, never inline.
func AppBuildCNBEnv(spec appbuilds.RunSpec) []EnvPair {
	return []EnvPair{
		{"CNB_PLATFORM_API", "0.14"}, {"CNB_EXPERIMENTAL_MODE", "warn"},
		{"HOME", "/work/cnb-home"}, {"APP_DIR", spec.ContextDir()},
	}
}

// AppBuildpackPhase is one CNB lifecycle phase's bash script; every argument in it is ours, never the user's.
type AppBuildpackPhase struct{ Name, Script string }

// AppBuildpackPhases are the lifecycle phases in order: analyze and export are the platform's side,
// detect, restore and build run as one phase with the app's code and the build-visible secrets.
func AppBuildpackPhases(runRef string) []AppBuildpackPhase {
	layers, layout := "-layers=/work/layers", "-layout -layout-dir="+appBuildLayoutDir
	app := `-app="$APP_DIR"`
	return []AppBuildpackPhase{
		{appBuildAnalyze, appInsideCheckout + " && " + lifecycle("analyzer", layers, "-run-image="+byTag(runRef), layout, appBuildImageRef)},
		{appBuildBuildpacks, lifecycle("detector", layers, app, "-platform="+appBuildPlatformDir) + " && " +
			lifecycle("restorer", layers, layout) + " && " + lifecycle("builder", layers, app, "-platform="+appBuildPlatformDir)},
		{appBuildExport, lifecycle("exporter", layers, app, layout, appBuildImageRef)},
	}
}

// appInsideCheckout refuses a root directory that resolves outside the checkout (exit 13), before any buildpack runs.
const appInsideCheckout = `real=$(realpath -e "$APP_DIR") && case "$real" in /work/src|/work/src/*) true;; *) echo "the root directory is not inside the repository"; exit 13;; esac`

// lifecycle is one CNB lifecycle phase's command line; every argument is ours, never the user's.
func lifecycle(phase string, args ...string) string {
	return "/cnb/lifecycle/" + phase + " " + strings.Join(args, " ")
}

// layoutPath is where the lifecycle looks for an image in a layout: registry/repository/tag.
// A pinned digest is dropped: crane fetches by it, the lifecycle reads the tag's path.
func layoutPath(ref string) string {
	repository, tag := byTag(ref), "latest"
	if colon := strings.LastIndex(repository, ":"); colon > strings.LastIndex(repository, "/") {
		repository, tag = repository[:colon], repository[colon+1:]
	}
	host, rest, found := strings.Cut(repository, "/")
	switch {
	case !found || !strings.ContainsAny(host, ".:") && host != "localhost":
		repository = "index.docker.io/" + repository
	case host == "docker.io":
		repository = "index.docker.io/" + rest
	}
	return path.Join(repository, tag)
}

// byTag is a reference without its pinned digest.
func byTag(ref string) string {
	tagged, _, _ := strings.Cut(ref, "@")
	return tagged
}

// appCollectContainer runs as whoever wrote the image: the exporter leaves its blobs readable by the CNB user only.
func appCollectContainer(options AppBuildOptions, spec appbuilds.RunSpec) corev1.Container {
	security := lockedDown()
	if spec.Builder == appbuilds.BuilderBuildpacks {
		security.RunAsUser, security.RunAsGroup = pointerTo(appBuildCNBUID), pointerTo(appBuildCNBUID)
	}
	return corev1.Container{
		Name: siteBuildCollect, Image: options.PrepareImage, Command: []string{"sh", "/scripts/collect.sh"},
		Env: []corev1.EnvVar{{Name: "BUILDER", Value: string(spec.Builder)},
			{Name: "IMAGE_DIR", Value: AppBuildImageDir()}},
		SecurityContext: security, Resources: guaranteed("250m", "128Mi"),
		VolumeMounts: append(workMounts(), scriptMount()),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"test", "-f", siteBuildCollectReady}}},
			PeriodSeconds: 2,
		},
	}
}

func appBuildVolumes(spec appbuilds.RunSpec) []corev1.Volume {
	disk, tmp := resource.MustParse(spec.Disk), resource.MustParse(siteBuildTmpSize)
	name := appBuildName(spec.BuildID)
	return []corev1.Volume{
		{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &disk}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmp}}},
		{Name: "scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: appBuildScriptsName}}}},
		{Name: "git-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name + cloneSecretSuffix}}},
		{Name: "build-secrets", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name + "-secrets"}}},
	}
}

// renderAppBuildSecrets: the build-visible secrets and the clone token, apart.
func renderAppBuildSecrets(options AppBuildOptions, spec appbuilds.RunSpec) (*corev1.Secret, *corev1.Secret) {
	labels := appBuildLabels(spec)
	values := make(map[string]string, len(spec.Secrets))
	for name, value := range spec.Secrets {
		values[name] = value
	}
	name := appBuildName(spec.BuildID)
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-secrets", Namespace: options.Namespace, Labels: labels}, StringData: values},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + cloneSecretSuffix, Namespace: options.Namespace, Labels: labels},
			StringData: map[string]string{"token": spec.CloneToken}}
}

// renderAppBuildScripts: the site build's fence and clone, and the app collect step.
func renderAppBuildScripts(namespace string) (*corev1.ConfigMap, error) {
	data := map[string]string{}
	for _, script := range []string{"fence.sh", "clone.sh"} {
		body, err := siteBuildScripts.ReadFile("site_build_scripts/" + script)
		if err != nil {
			return nil, fmt.Errorf("read build script %s: %w", script, err)
		}
		data[script] = string(body)
	}
	body, err := appBuildScripts.ReadFile("app_build_scripts/collect.sh")
	if err != nil {
		return nil, fmt.Errorf("read build script collect.sh: %w", err)
	}
	data["collect.sh"] = string(body)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: appBuildScriptsName, Namespace: namespace, Labels: map[string]string{siteBuildComponentKey: appBuildComponent}},
		Data:       data,
	}, nil
}

// renderAppBuildEgressPolicy is the site builds' fence, for the app build pods.
func renderAppBuildEgressPolicy(options AppBuildOptions) *networkingv1.NetworkPolicy {
	policy := renderSiteBuildEgressPolicy(SiteBuildOptions{Namespace: options.Namespace, ExtraDenyCIDRs: options.ExtraDenyCIDRs})
	policy.Name = appBuildEgressName
	policy.Labels = map[string]string{siteBuildComponentKey: appBuildComponent}
	policy.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{siteBuildComponentKey: appBuildComponent}}
	return policy
}

// renderAppBuildQuota backstops the dispatcher like the site builds' quota.
func renderAppBuildQuota(options AppBuildOptions, largest sitebuilds.BuildSize) *corev1.ResourceQuota {
	quota := renderSiteBuildQuota(SiteBuildOptions{Namespace: options.Namespace, Slots: options.Slots}, largest)
	quota.Name = appBuildQuotaName
	quota.Labels = map[string]string{siteBuildComponentKey: appBuildComponent}
	return quota
}

func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
