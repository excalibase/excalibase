package k8s

import (
	"embed"
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

// The Buildpacks steps, in order: analyze+restore and export are the platform's
// side; detect+build run the app's code and read the build-visible secrets.
const (
	appBuildAnalyze    = "analyze"
	appBuildBuildpacks = "buildpacks"
	appBuildExport     = "export"
	// appBuildPlatformDir/env holds the build-visible secrets, one file each (the CNB platform spec).
	appBuildPlatformDir = "/run/platform"
)

var appBuildPhases = []string{appBuildAnalyze, appBuildBuildpacks, appBuildExport}

// AppBuildOptions is where and how this install runs app builds.
type AppBuildOptions struct {
	Namespace     string
	RuntimeClass  string
	PriorityClass string
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

// appPrepareContainer is the site builds' root chmod, plus the directories the
// image builders write to, each sticky and open to the builders' two users.
func appPrepareContainer(image string) corev1.Container {
	prepare := prepareWorkdirContainer(image)
	dirs := "/work /tmp /work/out /work/layers /work/cnb-home"
	prepare.Command = []string{"sh", "-c", "mkdir -p " + dirs + " && chmod 1777 " + dirs}
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

// buildKitContainer runs one throwaway rootless buildkitd inside the sandbox
// (no daemon socket, nothing shared between builds) and writes an OCI archive.
// Secrets are files for RUN --mount=type=secret; without BuildKit's process
// sandbox any RUN step can read them too, so they are build-visible, not step-scoped.
// Build args are plain.
func buildKitContainer(options AppBuildOptions, spec appbuilds.RunSpec) corev1.Container {
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
	args = append(args, "--output", "type=oci,dest=/work/out/image.tar")
	security := lockedDown()
	// rootlesskit maps the build's user namespace through the setuid newuidmap,
	// inside gVisor: escalation on, and only the two capabilities it hands out.
	security.AllowPrivilegeEscalation = pointerTo(true)
	security.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SETUID", "SETGID"}}
	return corev1.Container{
		Name: appBuildBuildKit, Image: options.BuildKitImage, Command: []string{"sh", "-c", buildKitEntry, "buildkit", spec.ContextDir(), dockerfile},
		Args: args,
		Env: []corev1.EnvVar{
			{Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox --root /work/buildkit"},
			{Name: "HOME", Value: "/work/home"}, {Name: "XDG_RUNTIME_DIR", Value: "/tmp/run"},
		},
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
		Args:            []string{"pull", "--platform=" + options.platform(), "--format=oci", runRef, path.Join(appBuildLayoutDir, layoutPath(runRef))},
		SecurityContext: security, Resources: guaranteed("500m", "512Mi"), VolumeMounts: workMounts(),
	}
	layers, layout := "-layers=/work/layers", "-layout -layout-dir="+appBuildLayoutDir
	app := "-app=" + spec.ContextDir()
	phases := map[string]string{
		appBuildAnalyze: appInsideCheckout + " && " + lifecycle("analyzer", layers, "-run-image="+byTag(runRef), layout, appBuildImageRef) + " && " +
			lifecycle("restorer", layers, layout),
		appBuildBuildpacks: lifecycle("detector", layers, app, "-platform="+appBuildPlatformDir) + " && " +
			lifecycle("builder", layers, app, "-platform="+appBuildPlatformDir),
		appBuildExport: lifecycle("exporter", layers, app, layout, appBuildImageRef),
	}
	containers := []corev1.Container{fetch}
	for _, phase := range appBuildPhases {
		resources, mounts := guaranteed("500m", "512Mi"), workMounts()
		if phase == appBuildBuildpacks {
			resources = appBuildResources(spec)
			mounts = append(mounts, corev1.VolumeMount{Name: "build-secrets", MountPath: appBuildPlatformDir + "/env", ReadOnly: true})
		}
		containers = append(containers, corev1.Container{
			Name: phase, Image: options.BuilderImage, Command: []string{"bash", "-c", "set -e; " + phases[phase]},
			Env: []corev1.EnvVar{
				{Name: "CNB_PLATFORM_API", Value: "0.14"}, {Name: "CNB_EXPERIMENTAL_MODE", Value: "warn"},
				{Name: "HOME", Value: "/work/cnb-home"}, {Name: "APP_DIR", Value: spec.ContextDir()},
			},
			SecurityContext: security, Resources: resources, VolumeMounts: mounts,
		})
	}
	return containers
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

func appCollectContainer(options AppBuildOptions, spec appbuilds.RunSpec) corev1.Container {
	return corev1.Container{
		Name: siteBuildCollect, Image: options.PrepareImage, Command: []string{"sh", "/scripts/collect.sh"},
		Env: []corev1.EnvVar{{Name: "BUILDER", Value: string(spec.Builder)},
			{Name: "IMAGE_DIR", Value: path.Join(appBuildLayoutDir, layoutPath(appBuildImageRef))}},
		SecurityContext: lockedDown(), Resources: guaranteed("250m", "128Mi"),
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
		{Name: "git-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name + "-git"}}},
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
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-git", Namespace: options.Namespace, Labels: labels},
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
